/* Loadout fast receiver for the PS5.
 *
 * A tiny server that lets Loadout write one big file over several
 * connections at once. Each connection carries one slice of the file.
 *
 * Wire format (little endian):
 *   request : "TTRX" ver:u8 op:u8 pathlen:u16 offset:u64 length:u64 total:u64 path
 *   op 1 WRITE: reply status:u32 errno:u32, then the client streams `length`
 *               bytes; the server answers with a running u64 of bytes safely
 *               written (every ACK_EVERY bytes and at the end).
 *   op 2 PING : reply "TTRX" ver:u8 and close.
 *   op 3 QUIT : the server exits (a newer copy is taking over).
 *   WRITE with `total` > 0 asks the server to reserve the whole file first.
 */
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>

#define TT_PORT   9046
#define TT_VER    3
#define BUF_SIZE  (4u << 20)
#define ACK_EVERY (1ull << 20)
#define MIN_WRITE (1u << 20)  /* write once this much has arrived, so slow links still get steady confirmations */

#ifdef __PROSPERO__
typedef struct { char pad[45]; char message[3075]; } tt_notify_t;
int sceKernelSendNotificationRequest(int, tt_notify_t *, size_t, int);
static void notify(const char *msg) {
  tt_notify_t n;
  memset(&n, 0, sizeof(n));
  strncpy(n.message, msg, sizeof(n.message) - 1);
  sceKernelSendNotificationRequest(0, &n, sizeof(n), 0);
}
#else
static void notify(const char *msg) { puts(msg); }
#endif


static int read_full(int fd, void *p, size_t n) {
  uint8_t *b = p;
  while (n) {
    ssize_t r = recv(fd, b, n, 0);
    if (r == 0) return -1;
    if (r < 0) { if (errno == EINTR) continue; return -1; }
    b += r; n -= (size_t)r;
  }
  return 0;
}

static int write_full(int fd, const void *p, size_t n) {
  const uint8_t *b = p;
  while (n) {
    ssize_t r = send(fd, b, n, 0);
    if (r < 0) { if (errno == EINTR) continue; return -1; }
    b += r; n -= (size_t)r;
  }
  return 0;
}

static void reply(int fd, uint32_t status, uint32_t err) {
  uint32_t r[2] = { status, err };
  write_full(fd, r, sizeof(r));
}

static void do_write(int c, const char *path, uint64_t off, uint64_t len, uint64_t total) {
  int fd = open(path, O_CREAT | O_WRONLY, 0777);
  if (fd < 0) { reply(c, 1, (uint32_t)errno); return; }
  (void)total; /* no up-front reserve: on the PS5 it writes the whole file as zeros first */
  uint8_t *buf = malloc(BUF_SIZE);
  if (!buf) { close(fd); reply(c, 1, ENOMEM); return; }
  reply(c, 0, 0);

  uint64_t done = 0, acked = 0;
  int bad = 0;
  while (done < len && !bad) {
    size_t want = BUF_SIZE, have = 0;
    if (len - done < want) want = (size_t)(len - done);
    while (have < want && have < MIN_WRITE) {
      ssize_t r = recv(c, buf + have, want - have, 0);
      if (r < 0 && errno == EINTR) continue;
      if (r <= 0) { bad = 1; break; }
      have += (size_t)r;
    }
    if (have == 0) break;
    size_t put = 0;
    while (put < have) {
      ssize_t w = pwrite(fd, buf + put, have - put, (off_t)(off + done + put));
      if (w < 0 && errno == EINTR) continue;
      if (w <= 0) { bad = 2; break; }
      put += (size_t)w;
    }
    done += put;
    if (bad == 2) break;
    if (done - acked >= ACK_EVERY || done == len) {
      if (write_full(c, &done, sizeof(done))) { bad = 1; break; }
      acked = done;
    }
  }
  if (bad == 2) {
    /* disk trouble: tell the client with an impossible count */
    uint64_t fail = UINT64_MAX;
    write_full(c, &fail, sizeof(fail));
  }
  free(buf);
  close(fd);
}

static void *client(void *arg) {
  int c = (int)(long)arg;
  int opt = 4 << 20;
  setsockopt(c, SOL_SOCKET, SO_RCVBUF, &opt, sizeof(opt));
  opt = 1;
  setsockopt(c, IPPROTO_TCP, TCP_NODELAY, &opt, sizeof(opt));

  uint8_t h[32];
  char path[1100];
  size_t pre = 0;
  if (read_full(c, h, sizeof(h)) || memcmp(h, "TTRX", 4) || h[4] != TT_VER) goto out;
  uint16_t plen; uint64_t off, len, total;
  memcpy(&plen, h + 6, 2); memcpy(&off, h + 8, 8);
  memcpy(&len, h + 16, 8); memcpy(&total, h + 24, 8);
  switch (h[5]) {
  case 2:
    { uint8_t pong[5] = { 'T', 'T', 'R', 'X', TT_VER }; write_full(c, pong, 5); }
    break;
  case 3:
    close(c);
    _exit(0);
  case 1:
#ifndef __PROSPERO__
    if (getenv("TT_ROOT")) { strncpy(path, getenv("TT_ROOT"), 512); path[512] = 0; pre = strlen(path); }
#endif
    if (plen == 0 || plen > 512 || read_full(c, path + pre, plen)) goto out;
    path[pre + plen] = 0;
    if (path[pre] != '/' || strstr(path + pre, "/../")) { reply(c, 1, EINVAL); goto out; }
    do_write(c, path, off, len, total);
    break;
  }
out:
  close(c);
  return NULL;
}

static void ask_old_to_quit(void) {
  struct sockaddr_in a;
  /* older copies only listen to their own version number, so ask in each */
  for (uint8_t ver = 1; ver <= TT_VER; ver++) {
    uint8_t h[32] = { 'T', 'T', 'R', 'X', ver, 3 };
    int s = socket(AF_INET, SOCK_STREAM, 0);
    if (s < 0) return;
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_port = htons(TT_PORT);
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (connect(s, (struct sockaddr *)&a, sizeof(a)) == 0) write_full(s, h, sizeof(h));
    close(s);
  }
}

int main(void) {
  struct sockaddr_in a;
  int srv = -1, opt = 1;
  signal(SIGPIPE, SIG_IGN);
  for (int tries = 0; tries < 5; tries++) {
    srv = socket(AF_INET, SOCK_STREAM, 0);
    if (srv < 0) return 1;
    setsockopt(srv, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_port = htons(TT_PORT);
    a.sin_addr.s_addr = htonl(INADDR_ANY);
    if (bind(srv, (struct sockaddr *)&a, sizeof(a)) == 0) break;
    close(srv); srv = -1;
    ask_old_to_quit();
    sleep(1);
  }
  if (srv < 0 || listen(srv, 16)) { notify("Loadout receiver: port 9046 is busy"); return 1; }
  notify("Loadout fast receiver is ready");
  for (;;) {
    int c = accept(srv, NULL, NULL);
    if (c < 0) { if (errno == EINTR) continue; sleep(1); continue; }
    pthread_t t;
    pthread_attr_t at;
    pthread_attr_init(&at);
    pthread_attr_setdetachstate(&at, PTHREAD_CREATE_DETACHED);
    if (pthread_create(&t, &at, client, (void *)(long)c)) close(c);
    pthread_attr_destroy(&at);
  }
}
