#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>

#ifdef TCP_MSS
#undef TCP_MSS
#endif

#include "lwip/init.h"
#include "lwip/ip.h"
#include "lwip/ip4_addr.h"
#include "lwip/netif.h"
#include "lwip/pbuf.h"
#include "lwip/priv/tcp_priv.h"
#include "lwip/tcp.h"
#include "lwip/timeouts.h"

#define WG_ADDR "10.200.99.1"
#define HOST_ADDR "10.200.0.1"

static char host_dest[64] = HOST_ADDR;

/* guacd connects here. lwIP then dials the WireGuard peer. */
static int dial_enabled = 1;
static int dial_listen_fd = -1;
static char dial_listen_host[64] = "127.0.0.1";
static unsigned dial_listen_port = 3389;
static char dial_dest_host[64] = "10.200.99.2";
static unsigned dial_dest_port = 3389;
static ip4_addr_t dial_dest_ip;

/* The host process owns these: the WebSocket listener and the lifecycle hooks.
   The dial listen port is blocked so a peer connection to that port on
   10.200.99.1 cannot loop back into the dialer. */
int lwip_port_blocked(u16_t port) {
    return port == 8080 || port == 9000 || (dial_enabled && port == (u16_t)dial_listen_port);
}

static void load_host_dest(void) {
    const char *addr = getenv("WG_HOST_ADDR");
    if (addr != NULL && addr[0] != '\0') {
        snprintf(host_dest, sizeof(host_dest), "%s", addr);
    }
}
#define MAX_PKT 2048
#define IO_CAP 65535
#define MAX_FLOWS 8
#define LOG_SLOTS 64
#define LOG_LEN 160

struct flow {
    struct tcp_pcb *in;
    int fd;
    int connecting;
    int wg_fin;
    int host_fin;
    int wg_shut;
    int host_shut;
    unsigned host_port;
    uint8_t to_host[IO_CAP];
    uint16_t to_host_len;
    uint8_t to_wg[IO_CAP];
    uint16_t to_wg_len;
};

static struct netif wg_netif;
static struct flow *flows[MAX_FLOWS];
static struct queued_packet {
    struct queued_packet *next;
    uint16_t len;
    uint8_t data[MAX_PKT];
} *head, *tail;
static char logs[LOG_SLOTS][LOG_LEN];
static int log_r;
static int log_w;

u32_t sys_now(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (u32_t)(ts.tv_sec * 1000u + ts.tv_nsec / 1000000u);
}

static void note(const char *msg) {
    int next = (log_w + 1) % LOG_SLOTS;
    if (next == log_r) {
        return;
    }
    snprintf(logs[log_w], LOG_LEN, "%s", msg);
    log_w = next;
}

int lwip_next_log(char *buf, int cap) {
    if (log_r == log_w || cap <= 0) {
        return 0;
    }
    snprintf(buf, (size_t)cap, "%s", logs[log_r]);
    log_r = (log_r + 1) % LOG_SLOTS;
    return (int)strlen(buf);
}

static void enqueue(struct pbuf *p) {
    if (p->tot_len > MAX_PKT) {
        note("dropped oversized lwIP packet");
        return;
    }
    struct queued_packet *q = calloc(1, sizeof(*q));
    if (q == NULL) {
        note("out of memory queuing lwIP packet");
        return;
    }
    q->len = pbuf_copy_partial(p, q->data, p->tot_len, 0);
    if (tail == NULL) {
        head = tail = q;
    } else {
        tail->next = q;
        tail = q;
    }
}

static err_t wg_output(struct netif *netif, struct pbuf *p, const ip4_addr_t *addr) {
    LWIP_UNUSED_ARG(netif);
    LWIP_UNUSED_ARG(addr);
    enqueue(p);
    return ERR_OK;
}

static err_t wg_init(struct netif *netif) {
    netif->output = wg_output;
    netif->mtu = 1280;
    netif->flags = NETIF_FLAG_UP | NETIF_FLAG_LINK_UP;
    return ERR_OK;
}

static void forget_flow(struct flow *f) {
    for (int i = 0; i < MAX_FLOWS; i++) {
        if (flows[i] == f) {
            flows[i] = NULL;
        }
    }
}

static void close_fd(struct flow *f) {
    if (f->fd >= 0) {
        close(f->fd);
        f->fd = -1;
    }
}

static int aborted_pcb;

static int flow_alive(struct flow *f) {
    for (int i = 0; i < MAX_FLOWS; i++) {
        if (flows[i] == f) {
            return 1;
        }
    }
    return 0;
}

static void destroy_flow(struct flow *f, int abort_pcb) {
    struct tcp_pcb *pcb = f->in;
    close_fd(f);
    f->in = NULL;
    aborted_pcb = 0;
    if (pcb != NULL) {
        tcp_arg(pcb, NULL);
        tcp_recv(pcb, NULL);
        tcp_sent(pcb, NULL);
        tcp_err(pcb, NULL);
        if (abort_pcb || tcp_close(pcb) != ERR_OK) {
            aborted_pcb = 1;
            tcp_abort(pcb);
        }
    }
    forget_flow(f);
    free(f);
}

static void in_err(void *arg, err_t err) {
    struct flow *f = arg;
    LWIP_UNUSED_ARG(err);
    if (f == NULL) {
        return;
    }
    f->in = NULL;
    close_fd(f);
    forget_flow(f);
    free(f);
}

static void flush_to_host(struct flow *f) {
    while (f->fd >= 0 && !f->connecting && f->to_host_len > 0) {
        ssize_t n = send(f->fd, f->to_host, f->to_host_len, MSG_NOSIGNAL);
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) {
                return;
            }
            note("host socket send failed");
            destroy_flow(f, 1);
            return;
        }
        if (n == 0) {
            return;
        }
        memmove(f->to_host, f->to_host + n, f->to_host_len - (uint16_t)n);
        f->to_host_len -= (uint16_t)n;
    }
    if (f->wg_fin && f->to_host_len == 0 && !f->host_shut && f->fd >= 0 && !f->connecting) {
        shutdown(f->fd, SHUT_WR);
        f->host_shut = 1;
    }
}

static void flush_to_wg(struct flow *f) {
    struct tcp_pcb *pcb = f->in;
    if (pcb == NULL) {
        return;
    }
    while (f->to_wg_len > 0 && pcb->state == ESTABLISHED) {
        u16_t space = tcp_sndbuf(pcb);
        if (space == 0) {
            break;
        }
        u16_t n = f->to_wg_len < space ? f->to_wg_len : space;
        if (n > TCP_MSS) {
            n = TCP_MSS;
        }
        if (tcp_write(pcb, f->to_wg, n, TCP_WRITE_FLAG_COPY) != ERR_OK) {
            break;
        }
        memmove(f->to_wg, f->to_wg + n, f->to_wg_len - n);
        f->to_wg_len -= n;
    }
    tcp_output(pcb);
    if (f->host_fin && f->to_wg_len == 0 && !f->wg_shut && f->in != NULL) {
        if (tcp_shutdown(f->in, 0, 1) == ERR_OK) {
            f->wg_shut = 1;
        }
    }
}

static void settle(struct flow *f) {
    if (!flow_alive(f)) {
        return;
    }
    flush_to_host(f);
    if (!flow_alive(f)) {
        return;
    }
    flush_to_wg(f);
    if (!flow_alive(f)) {
        return;
    }
    if (f->host_shut && f->host_fin && f->wg_fin && f->wg_shut && f->to_host_len == 0 && f->to_wg_len == 0) {
        destroy_flow(f, 0);
    }
}

static void take_socket_data(struct flow *f) {
    while (f->fd >= 0 && !f->host_fin && f->to_wg_len < IO_CAP) {
        uint8_t tmp[2048];
        size_t space = (size_t)(IO_CAP - f->to_wg_len);
        if (space > sizeof(tmp)) {
            space = sizeof(tmp);
        }
        ssize_t n = recv(f->fd, tmp, space, 0);
        if (n == 0) {
            f->host_fin = 1;
            note("host socket closed");
            break;
        }
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) {
                break;
            }
            note("host socket recv failed");
            destroy_flow(f, 1);
            return;
        }
        memcpy(f->to_wg + f->to_wg_len, tmp, (size_t)n);
        f->to_wg_len += (uint16_t)n;
    }
}

static void finish_connect(struct flow *f) {
    int soerr = 0;
    socklen_t sl = sizeof(soerr);
    if (getsockopt(f->fd, SOL_SOCKET, SO_ERROR, &soerr, &sl) < 0 || soerr != 0) {
        note("host socket connect failed");
        destroy_flow(f, 1);
        return;
    }
    f->connecting = 0;
    note("host socket connected");
}

static void drive(struct flow *f, short revents) {
    if (!flow_alive(f) || f->fd < 0) {
        settle(f);
        return;
    }
    if (f->connecting && (revents & (POLLOUT | POLLERR | POLLHUP | POLLNVAL))) {
        finish_connect(f);
        if (!flow_alive(f)) {
            return;
        }
    }
    if (!f->connecting && (revents & (POLLIN | POLLERR | POLLHUP))) {
        take_socket_data(f);
        if (!flow_alive(f)) {
            return;
        }
    }
    settle(f);
}

static err_t in_recv(void *arg, struct tcp_pcb *pcb, struct pbuf *p, err_t err) {
    struct flow *f = arg;
    if (f == NULL || err != ERR_OK) {
        return err;
    }
    if (p == NULL) {
        f->wg_fin = 1;
        settle(f);
        if (!flow_alive(f)) {
            return aborted_pcb ? ERR_ABRT : ERR_OK;
        }
        return ERR_OK;
    }
    if ((size_t)f->to_host_len + p->tot_len > IO_CAP) {
        return ERR_MEM;
    }
    pbuf_copy_partial(p, f->to_host + f->to_host_len, p->tot_len, 0);
    f->to_host_len += p->tot_len;
    tcp_recved(pcb, p->tot_len);
    pbuf_free(p);
    settle(f);
    if (!flow_alive(f)) {
        return aborted_pcb ? ERR_ABRT : ERR_OK;
    }
    return ERR_OK;
}

static err_t in_sent(void *arg, struct tcp_pcb *pcb, u16_t len) {
    LWIP_UNUSED_ARG(pcb);
    LWIP_UNUSED_ARG(len);
    settle(arg);
    if (!flow_alive(arg)) {
        return aborted_pcb ? ERR_ABRT : ERR_OK;
    }
    return ERR_OK;
}

static int open_host_socket(struct flow *f) {
    if (lwip_port_blocked((u16_t)f->host_port)) {
        return -1;
    }
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) {
        return -1;
    }
    int flags = fcntl(fd, F_GETFL, 0);
    if (flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0) {
        close(fd);
        return -1;
    }
    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));

    struct sockaddr_in dest;
    memset(&dest, 0, sizeof(dest));
    dest.sin_family = AF_INET;
    dest.sin_port = htons(f->host_port);
    if (inet_pton(AF_INET, host_dest, &dest.sin_addr) != 1) {
        close(fd);
        return -1;
    }
    int rc = connect(fd, (struct sockaddr *)&dest, sizeof(dest));
    if (rc < 0 && errno != EINPROGRESS) {
        close(fd);
        return -1;
    }
    f->fd = fd;
    f->connecting = (rc < 0);
    return 0;
}

static err_t on_accept(void *arg, struct tcp_pcb *pcb, err_t err) {
    LWIP_UNUSED_ARG(arg);
    if (err != ERR_OK || pcb == NULL) {
        return ERR_VAL;
    }
    if (lwip_port_blocked(pcb->local_port)) {
        char msg[80];
        snprintf(msg, sizeof(msg), "blocked tcp/%u", (unsigned)pcb->local_port);
        note(msg);
        tcp_abort(pcb);
        return ERR_ABRT;
    }
    int slot = -1;
    for (int i = 0; i < MAX_FLOWS; i++) {
        if (flows[i] == NULL) {
            slot = i;
            break;
        }
    }
    struct flow *f = slot < 0 ? NULL : calloc(1, sizeof(*f));
    if (f == NULL) {
        note("no room for another forwarded connection");
        return ERR_MEM;
    }
    f->fd = -1;
    f->in = pcb;
    f->host_port = pcb->local_port;
    flows[slot] = f;
    tcp_arg(pcb, f);
    tcp_err(pcb, in_err);
    tcp_recv(pcb, in_recv);
    tcp_sent(pcb, in_sent);
    tcp_nagle_disable(pcb);
    if (open_host_socket(f) != 0) {
        note("host socket connect failed");
        tcp_arg(pcb, NULL);
        tcp_err(pcb, NULL);
        tcp_recv(pcb, NULL);
        tcp_sent(pcb, NULL);
        f->in = NULL;
        forget_flow(f);
        free(f);
        return ERR_MEM;
    }
    char msg[160];
    snprintf(msg, sizeof(msg), "accepted tcp/%u, %s host socket to %s:%u",
             (unsigned)pcb->local_port, f->connecting ? "connecting" : "connected",
             host_dest, f->host_port);
    note(msg);
    return ERR_OK;
}

static int listen_any(const ip4_addr_t *addr) {
    struct tcp_pcb *pcb = tcp_new();
    if (pcb == NULL) {
        return -1;
    }
    /* tcp_bind assigns an ephemeral port when asked for 0. Bind a
       placeholder, then mark this listener as any-port. tcp_input treats
       local_port 0 as a match for every destination port except 8080 and
       9000, and copies the real port onto the accepted connection. */
    if (tcp_bind(pcb, addr, 1) != ERR_OK) {
        tcp_close(pcb);
        return -1;
    }
    tcp_bind_netif(pcb, &wg_netif);
    pcb = tcp_listen(pcb);
    if (pcb == NULL) {
        return -1;
    }
    pcb->local_port = 0;
    tcp_accept(pcb, on_accept);
    return 0;
}

static int parse_host_port(const char *text, char *host, size_t host_cap, unsigned *port) {
    const char *colon = strrchr(text, ':');
    if (colon == NULL || colon == text) {
        return -1;
    }
    size_t n = (size_t)(colon - text);
    if (n == 0 || n >= host_cap) {
        return -1;
    }
    memcpy(host, text, n);
    host[n] = '\0';
    char *end = NULL;
    long value = strtol(colon + 1, &end, 10);
    if (end == colon + 1 || *end != '\0' || value <= 0 || value > 65535) {
        return -1;
    }
    *port = (unsigned)value;
    return 0;
}

static int load_dial(void) {
    const char *listen = getenv("WG_DIAL_LISTEN");
    const char *dest = getenv("WG_DIAL_DEST");
    if (listen != NULL && (listen[0] == '\0' || strcmp(listen, "off") == 0)) {
        dial_enabled = 0;
        return 0;
    }
    if (listen != NULL &&
        parse_host_port(listen, dial_listen_host, sizeof(dial_listen_host), &dial_listen_port) != 0) {
        note("WG_DIAL_LISTEN is not host:port");
        return -1;
    }
    if (dest != NULL &&
        parse_host_port(dest, dial_dest_host, sizeof(dial_dest_host), &dial_dest_port) != 0) {
        note("WG_DIAL_DEST is not host:port");
        return -1;
    }
    if (!ip4addr_aton(dial_dest_host, &dial_dest_ip)) {
        note("dial destination is not ipv4");
        return -1;
    }
    return 0;
}

static err_t dial_connected(void *arg, struct tcp_pcb *pcb, err_t err) {
    struct flow *f = arg;
    LWIP_UNUSED_ARG(pcb);
    LWIP_UNUSED_ARG(err);
    if (!flow_alive(f)) {
        return ERR_OK;
    }
    note("rdp peer connected");
    settle(f);
    if (!flow_alive(f)) {
        return aborted_pcb ? ERR_ABRT : ERR_OK;
    }
    return ERR_OK;
}

static void start_dial(int fd) {
    int slot = -1;
    for (int i = 0; i < MAX_FLOWS; i++) {
        if (flows[i] == NULL) {
            slot = i;
            break;
        }
    }
    if (slot < 0) {
        note("no room for a dialed connection");
        close(fd);
        return;
    }
    struct flow *f = calloc(1, sizeof(*f));
    struct tcp_pcb *pcb = tcp_new();
    if (f == NULL || pcb == NULL) {
        note("dial out of memory");
        if (pcb != NULL) {
            tcp_abort(pcb);
        }
        free(f);
        close(fd);
        return;
    }
    f->fd = fd;
    f->in = pcb;
    f->host_port = dial_dest_port;
    flows[slot] = f;
    tcp_arg(pcb, f);
    tcp_err(pcb, in_err);
    tcp_recv(pcb, in_recv);
    tcp_sent(pcb, in_sent);
    tcp_nagle_disable(pcb);
    tcp_bind_netif(pcb, &wg_netif);
    char msg[160];
    snprintf(msg, sizeof(msg), "dialing %s:%u", dial_dest_host, dial_dest_port);
    note(msg);
    err_t e = tcp_connect(pcb, &dial_dest_ip, (u16_t)dial_dest_port, dial_connected);
    if (e != ERR_OK) {
        note("dial tcp_connect failed");
        destroy_flow(f, 1);
    }
}

static void accept_dials(void) {
    if (dial_listen_fd < 0) {
        return;
    }
    for (;;) {
        int fd = accept(dial_listen_fd, NULL, NULL);
        if (fd < 0) {
            if (errno == EINTR) {
                continue;
            }
            return;
        }
        int flags = fcntl(fd, F_GETFL, 0);
        if (flags >= 0) {
            fcntl(fd, F_SETFL, flags | O_NONBLOCK);
        }
        int clo = fcntl(fd, F_GETFD, 0);
        if (clo >= 0) {
            fcntl(fd, F_SETFD, clo | FD_CLOEXEC);
        }
        int one = 1;
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));
        start_dial(fd);
    }
}

static int listen_dial(void) {
    int fd = socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) {
        note("dial socket failed");
        return -1;
    }
    int one = 1;
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    int flags = fcntl(fd, F_GETFL, 0);
    if (flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0) {
        close(fd);
        note("dial socket flags failed");
        return -1;
    }
    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_port = htons((uint16_t)dial_listen_port);
    if (inet_pton(AF_INET, dial_listen_host, &addr.sin_addr) != 1) {
        close(fd);
        note("dial listen address is not ipv4");
        return -1;
    }
    if (bind(fd, (struct sockaddr *)&addr, sizeof(addr)) < 0 || listen(fd, 8) < 0) {
        close(fd);
        note("dial listen failed");
        return -1;
    }
    dial_listen_fd = fd;
    char msg[160];
    snprintf(msg, sizeof(msg), "dial %s:%u -> %s:%u", dial_listen_host, dial_listen_port,
             dial_dest_host, dial_dest_port);
    note(msg);
    return 0;
}

int lwip_startup(void) {
    char msg[160];
    load_host_dest();
    if (load_dial() != 0) {
        return -1;
    }
    lwip_init();
    ip4_addr_t addr;
    ip4_addr_t mask;
    ip4_addr_t gw;
    ip4addr_aton(WG_ADDR, &addr);
    ip4addr_aton("255.255.255.255", &mask);
    ip4_addr_set_any(&gw);
    if (netif_add(&wg_netif, &addr, &mask, &gw, NULL, wg_init, ip_input) == NULL) {
        return -1;
    }
    netif_set_up(&wg_netif);
    netif_set_link_up(&wg_netif);
    netif_set_default(&wg_netif);

    if (listen_any(&addr) != 0) {
        return -1;
    }
    if (dial_enabled && listen_dial() != 0) {
        return -1;
    }
    snprintf(msg, sizeof(msg), "lwIP listening on " WG_ADDR ", host socket -> %s:<same port>",
             host_dest);
    note(msg);
    return 0;
}

int lwip_inject(const uint8_t *ip, uint32_t len) {
    if (ip == NULL || len == 0 || len > 65535) {
        return -1;
    }
    struct pbuf *p = pbuf_alloc(PBUF_RAW, (u16_t)len, PBUF_RAM);
    if (p == NULL) {
        return -1;
    }
    pbuf_take(p, ip, (u16_t)len);
    err_t e = ip_input(p, &wg_netif);
    return e == ERR_OK ? 0 : -1;
}

void lwip_poll(void) {
    accept_dials();
    struct pollfd pf[MAX_FLOWS];
    int map[MAX_FLOWS];
    int n = 0;
    for (int i = 0; i < MAX_FLOWS; i++) {
        struct flow *f = flows[i];
        if (f == NULL || f->fd < 0) {
            continue;
        }
        pf[n].fd = f->fd;
        pf[n].events = POLLIN;
        if (f->connecting || f->to_host_len > 0) {
            pf[n].events |= POLLOUT;
        }
        pf[n].revents = 0;
        map[n] = i;
        n++;
    }
    if (n > 0 && poll(pf, (nfds_t)n, 0) > 0) {
        for (int k = 0; k < n; k++) {
            if (pf[k].revents == 0) {
                continue;
            }
            struct flow *f = flows[map[k]];
            if (f != NULL) {
                drive(f, pf[k].revents);
            }
        }
    }
    for (int i = 0; i < MAX_FLOWS; i++) {
        if (flows[i] != NULL) {
            settle(flows[i]);
        }
    }
    sys_check_timeouts();
}

int lwip_next_packet(uint8_t *buf, uint32_t cap) {
    if (head == NULL || buf == NULL) {
        return 0;
    }
    struct queued_packet *q = head;
    head = q->next;
    if (head == NULL) {
        tail = NULL;
    }
    uint32_t n = q->len;
    if (n > cap) {
        n = cap;
    }
    memcpy(buf, q->data, n);
    int full = q->len;
    free(q);
    return full <= (int)cap ? full : -1;
}
