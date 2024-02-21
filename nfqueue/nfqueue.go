// Go bindings for the NFQUEUE netfilter target
// libnetfilter_queue is a userspace library providing an API to access packets
// that have been queued by the Linux kernel packet filter.
//
// This provides an easy way to filter packets from userspace, and use tools
// or libraries that are not accessible from kernelspace.

package nfqueue

/*
#cgo pkg-config: libnetfilter_queue

#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include <errno.h>
#include <arpa/inet.h>
#include <linux/netfilter.h>
#include <pthread.h>
#include <time.h>
#include <libnetfilter_queue/libnetfilter_queue.h>
#include <stdint.h>

extern int GoCallbackWrapper(void *data, void *nfad);
static inline ssize_t recv_to(int sockfd, void *buf, size_t len, int flags, int to);

pthread_mutex_t read_mutex;
pthread_mutex_t write_mutex;

int _process_loop(struct nfq_handle *h,
                  int *fd,
                  int flags,
                  int max_count,
                  uint64_t buf_len) {
        int rv = 0, count = 0, ret = 0;
        void *buf = malloc(buf_len);

        if (buf == NULL) {
            fprintf(stderr, "Could not allocate enough memory (%llu byte)for the buffer\n", buf_len);
        }

        (*fd) = nfq_fd(h);
        if (fd < 0) {
            free(buf);
            return -1;
        }

        //avoid ENOBUFS on read() operation, otherwise the while loop is interrupted.
        int opt = 1;
        rv = setsockopt(*fd, SOL_NETLINK, NETLINK_NO_ENOBUFS, &opt, sizeof(int));
        if (rv == -1) {
            free(buf);
            return -1;
        }

        while (h && *fd != -1) {
            pthread_mutex_lock(&read_mutex);
            rv = recv_to(*fd, buf, buf_len, flags, 500);
            if (rv > 0) {
                ret = nfq_handle_packet(h, buf, rv);
                pthread_mutex_unlock(&read_mutex);
                count++;
                if (max_count > 0 && count >= max_count) {
                    break;
                }
            } else if (rv < 0){
                pthread_mutex_unlock(&read_mutex);
                free(buf);
                return rv;
            }
            if (ret < 0) {
                pthread_mutex_unlock(&read_mutex);
                free(buf);
                return ret;
            }
            pthread_mutex_unlock(&read_mutex);
        }
        pthread_mutex_unlock(&read_mutex);
        free(buf);
        return count;
}

void _stop_loop(int *fd) {
    (*fd) = -1;
}

// recv with timeout using select
static inline ssize_t recv_to(int sockfd, void *buf, size_t len, int flags, int to) {
    int rv;
    ssize_t result;
    fd_set readset;

    // Initialize timeval struct
    struct timeval timeout;
    timeout.tv_sec = 0;
    timeout.tv_usec = to * 1000;

    // Initialize socket set
    FD_ZERO(&readset);
    FD_SET(sockfd, &readset);

    rv = select(sockfd+1, &readset, (fd_set *) 0, (fd_set *) 0, &timeout);
    // Check status
    if (rv < 0 && errno != EINTR) {
        return -1;
    } else if (rv > 0 && FD_ISSET(sockfd, &readset)) {
        // Receive (ensure that the socket is set to non blocking mode!)
        result = recv(sockfd, buf, len, flags);
        return result;
    }
    return 0;
}

int c_nfq_cb(struct nfq_q_handle *qh,
             struct nfgenmsg *nfmsg,
             struct nfq_data *nfad, void *data) {
    return GoCallbackWrapper(data, nfad);
}

// wrap nfq_get_payload so cgo always have the same prototype
// (libnetfilter_queue 0.17 uses a signed char)
static int _c_get_payload (struct nfq_data *nfad, unsigned char **data)
{
    return nfq_get_payload (nfad, data);
}
*/
import "C"

import (
    "errors"
     log "github.com/cyberys/rlog"
    "unsafe"
    "syscall"
)

var ErrNotInitialized = errors.New("nfqueue: queue not initialized")
var ErrOpenFailed = errors.New("nfqueue: open failed")
var ErrRuntime = errors.New("nfqueue: runtime error")

var NF_DROP = C.NF_DROP
var NF_ACCEPT = C.NF_ACCEPT
var NF_QUEUE = C.NF_QUEUE
var NF_REPEAT = C.NF_REPEAT
var NF_STOP = C.NF_STOP

var NFQA_CFG_F_FAIL_OPEN  uint32  = C.NFQA_CFG_F_FAIL_OPEN
var NFQA_CFG_F_CONNTRACK  uint32  = C.NFQA_CFG_F_CONNTRACK
/* These three variables seem not to be determinable by cgo, so they are statically
 * defined here. The values are taken from the ĺibnetfilter_queue source code (They are macros)
 */
var NFQA_CFG_F_GSO        uint32  = 1<<2
var NFQA_CFG_F_UID_GID    uint32  = 1<<3
var NFQA_CFG_F_SECCTX     uint32  = 1<<4

var NFQNL_COPY_NONE uint8   = C.NFQNL_COPY_NONE
var NFQNL_COPY_META uint8   = C.NFQNL_COPY_META
var NFQNL_COPY_PACKET uint8 = C.NFQNL_COPY_PACKET

var defaultBufLen C.uint64_t = 65535
// Prototype for a NFQUEUE callback.
// The callback receives the NFQUEUE ID of the packet, and
// the packet payload.
// Packet data start from the IP layer (ethernet information are not included).
// It should return -1 on error to stop processing or >= 0 otherwise.
type Callback func(*Payload) error

// Queue is an opaque structure describing a connection to a kernel NFQUEUE,
// and the associated Go callback.
type Queue struct {
    c_h  (*C.struct_nfq_handle)
    c_qh (*C.struct_nfq_q_handle)
    c_fd (*C.int)
    c_buf_len C.uint64_t

    cb Callback
}

// Init creates a netfilter queue which can be used to receive packets
// from the kernel.
func (q *Queue) Init() error {
    //log.Println("Opening queue")
    q.c_h = C.nfq_open()
    if (q.c_h == nil) {
        log.Error("nfq_open failed")
        return ErrOpenFailed
    }
    C.pthread_mutex_init(&C.read_mutex, nil)
    C.pthread_mutex_init(&C.write_mutex, nil)
    q.c_fd = (*C.int)(C.malloc(C.sizeof_int))
    q.c_buf_len = defaultBufLen
    return nil
}

// SetCallback sets the callback function, fired when a packet is received.
// Returning from the callback and evaluation its return value races with the netlink socket
// receiving a new packet from the kernel. Do not rely on the execution of the callback,
// at the end of which you return a negative value, to be the last execution of it.
func (q *Queue) SetCallback(cb Callback) error {
    q.cb = cb
    return nil
}

func (q *Queue) Close() {
    if (q.c_h != nil) {
        C.nfq_close(q.c_h)
        q.c_h = nil
    }
    C.pthread_mutex_destroy(&C.read_mutex)
    C.pthread_mutex_destroy(&C.write_mutex)
    C.free(unsafe.Pointer(q.c_fd))
}

// Bind binds a Queue to a given protocol family.
//
// Usually, the family is syscall.AF_INET for IPv4, and syscall.AF_INET6 for IPv6
func (q *Queue) Bind(af_family int) error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    /* Errors in nfq_bind_pf are non-fatal ...
     * This function just tells the kernel that nfnetlink_queue is
     * the chosen module to queue packets to userspace.
     */
    _ = C.nfq_bind_pf(q.c_h,C.u_int16_t(af_family))
    return nil
}

// Unbind a queue from the given protocol family.
//
// Note that errors from this function can usually be ignored.
func (q *Queue) Unbind(af_family int) error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    rc := C.nfq_unbind_pf(q.c_h,C.u_int16_t(af_family))
    if (rc < 0) {
        log.Error("nfq_unbind_pf failed")
        return ErrRuntime
    }
    return nil
}

// Create a new queue handle
//
// The queue must be initialized (using Init) and bound (using Bind), and
// a callback function must be set (using SetCallback).
func (q *Queue) CreateQueue(queue_num uint16) error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    if (q.cb == nil) {
        return ErrNotInitialized
    }
    q.c_qh = C.nfq_create_queue(q.c_h,C.u_int16_t(queue_num),(*C.nfq_callback)(C.c_nfq_cb),unsafe.Pointer(q))
    if (q.c_qh == nil) {
        log.Error("nfq_create_queue failed %v", queue_num)
        return ErrRuntime
    }
    // Default mode
    C.nfq_set_mode(q.c_qh,C.NFQNL_COPY_PACKET,0xffff)
    return nil
}

// Destroy a queue handle
//
// This also unbind from the nfqueue handler, so you don't have to call Unbind()
// Note that errors from this function can usually be ignored.
// WARNING: This function races with netlink sending messages and triggering the execution of the callback function!
// DO NOT rely on the callback function's negative return value to stop the netlink lib from calling the callback (because it doesn't)
func (q *Queue) DestroyQueue() error {
    if (q.c_qh == nil) {
        return ErrNotInitialized
    }
    rc := C.nfq_destroy_queue(q.c_qh)
    if (rc < 0) {
        log.Error("nfq_destroy_queue failed")
        return ErrRuntime
    }
    q.c_qh = nil
    return nil
}

// SetQueueBuffer sets the default socket buffer size with nfnl_rcvbufsiz.

func (q *Queue) SetQueueBuffer(buffersize uint32) error {
    if q.c_h == nil {
        return ErrNotInitialized
    }

    if q.c_qh == nil {
        return ErrNotInitialized
    }

    C.nfnl_rcvbufsiz(C.nfq_nfnlh(q.c_h), C.uint(buffersize))

    return nil
}

// SetBufLen sets the buffer length for receiving packets from netlink socket.

func (q *Queue) SetBufLen(bufLen C.uint64_t) error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    q.c_buf_len = C.uint64_t(bufLen)
    return nil
}
// SetMode sets the amount of packet data that nfqueue copies to userspace
//
// Default mode is NFQNL_COPY_PACKET
func (q *Queue) SetMode(mode uint8) error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    if (q.c_qh == nil) {
        return ErrNotInitialized
    }
    if C.nfq_set_mode(q.c_qh,C.u_int8_t(mode),0xffff) == -1 {
        return errors.New("Could not set queue mode")
    } else {
        return nil
    }
}


func (q *Queue) SetQueueFlags(mask, flags uint32) error {
    if q.c_h == nil {
        return ErrNotInitialized
    }

    if q.c_qh == nil {
        return ErrNotInitialized
    }

    _, err := C.nfq_set_queue_flags(q.c_qh, C.uint32_t(mask), C.uint32_t(flags))
    return err
}

// SetQueueMaxLen fixes the number of packets the kernel will store before internally before dropping upcoming packets
func (q *Queue) SetQueueMaxLen(maxlen uint32) error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    if (q.c_qh == nil) {
        return ErrNotInitialized
    }
    if C.nfq_set_queue_maxlen(q.c_qh,C.u_int32_t(maxlen)) == -1 {
        return errors.New("Could not set maximum queue length")
    } else {
        return nil
    }
}

// Main loop: Loop starts a loop, receiving kernel events
// and processing packets using the callback function.
func (q *Queue) Loop() error {
    if (q.c_h == nil) {
        return ErrNotInitialized
    }
    if (q.c_qh == nil) {
        return ErrNotInitialized
    }
    if (q.cb == nil) {
        return ErrNotInitialized
    }

    ret := C._process_loop(q.c_h, q.c_fd, 0, -1, q.c_buf_len)

    if ret < 0 {
        return ErrRuntime
    }
    return nil
}

func (q *Queue) StopLoop() {
    C._stop_loop(q.c_fd)
}

// Payload is a structure describing a packet received from the kernel
type Payload struct {
    c_qh (*C.struct_nfq_q_handle)
    nfad *C.struct_nfq_data

    // NFQueue ID of the packet
    Id uint32
    // Packet data
    Data []byte
}

func build_payload(c_qh *C.struct_nfq_q_handle, ptr_nfad *unsafe.Pointer) *Payload {
    var payload_data *C.uchar
    var data []byte

    nfad := (*C.struct_nfq_data)(unsafe.Pointer(ptr_nfad))

    ph := C.nfq_get_msg_packet_hdr(nfad)
    id := C.ntohl(C.uint32_t(ph.packet_id))
    payload_len := C._c_get_payload(nfad, &payload_data)
    if (payload_len >= 0) {
        data = C.GoBytes(unsafe.Pointer(payload_data), C.int(payload_len))
    }

    p := new(Payload)
    p.c_qh = c_qh
    p.nfad = nfad
    p.Id = uint32(id)
    p.Data = data

    return p
}

/* GetTimestamp gives you the timestamp of the packet.
 *
 */
func (p *Payload) GetTimestamp() syscall.Timeval {
    Ctime := (*C.struct_timeval)(C.malloc(C.sizeof_struct_timeval))
    var timeval syscall.Timeval
    C.nfq_get_timestamp(p.nfad, Ctime)
    timeval.Sec = int64(Ctime.tv_sec)
    timeval.Usec = int64(Ctime.tv_usec)
    C.free(unsafe.Pointer(Ctime))
    return timeval
}
// SetVerdict issues a verdict for a packet.
//
// Every queued packet _must_ have a verdict specified by userspace.
func (p *Payload) SetVerdict(verdict int) error {
    C.pthread_mutex_lock(&C.write_mutex)
    //log.Printf("Setting verdict for packet %d: %d\n",p.Id,verdict)
    C.nfq_set_verdict(p.c_qh,C.u_int32_t(p.Id),C.u_int32_t(verdict),0,nil)
    C.pthread_mutex_unlock(&C.write_mutex)
    return nil
}

// SetVerdictMark issues a verdict for a packet, but a mark can be set
//
// Every queued packet _must_ have a verdict specified by userspace.
func (p *Payload) SetVerdictMark(verdict int, mark uint32) error {
    //log.Printf("Setting verdict for packet %d: %d mark %lx\n",p.Id,verdict,mark)
    C.pthread_mutex_lock(&C.write_mutex)
    C.nfq_set_verdict2(
        p.c_qh,
        C.u_int32_t(p.Id),
        C.u_int32_t(verdict),
        C.u_int32_t(mark),
        0,nil)
    C.pthread_mutex_unlock(&C.write_mutex)
    return nil
}

// SetVerdictModified issues a verdict for a packet, but replaces the packet
// with the provided one.
//
// Every queued packet _must_ have a verdict specified by userspace.
func (p *Payload) SetVerdictModified(verdict int, data []byte) error {
    //log.Printf("Setting verdict for NEW packet %d: %d\n",p.Id,verdict)
    C.pthread_mutex_lock(&C.write_mutex)
    C.nfq_set_verdict(
        p.c_qh,
        C.u_int32_t(p.Id),
        C.u_int32_t(verdict),
        C.u_int32_t(len(data)),
        (*C.uchar)(unsafe.Pointer(&data[0])),
    )
    C.pthread_mutex_unlock(&C.write_mutex)
    return nil
}

// Returns the packet mark
func (p *Payload) GetNFMark() uint32 {
    return uint32(C.nfq_get_nfmark(p.nfad))
}

// Returns the interface that the packet was received through
func (p *Payload) GetInDev() uint32 {
    return uint32(C.nfq_get_indev(p.nfad))
}

// Returns the interface that the packet will be routed out
func (p *Payload) GetOutDev() uint32 {
    return uint32(C.nfq_get_outdev(p.nfad))
}

// Returns the physical interface that the packet was received through
func (p *Payload) GetPhysInDev() uint32 {
    return uint32(C.nfq_get_physindev(p.nfad))
}

// Returns the physical interface that the packet will be routed out
func (p *Payload) GetPhysOutDev() uint32 {
    return uint32(C.nfq_get_physoutdev(p.nfad))
}
