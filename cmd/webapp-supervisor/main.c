/* Linux-only, unprivileged WebApp sandbox capability probe.
 * The Go executable remains CGO_ENABLED=0. Never runs application code.
 * Restriction checks execute in a disposable child, not the caller's threads.
 */
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <unistd.h>

#define PROTOCOL_VERSION 1
#define REQUIRED_ABI 6
#define FS_READ_FILE (1ULL << 2)
#define FS_READ_DIR (1ULL << 3)
/* All ABI 6 filesystem rights, including REFER, TRUNCATE and IOCTL_DEV. */
#define FS_HANDLED ((1ULL << 16) - 1)
#define NET_BIND_TCP (1ULL << 0)
#define NET_CONNECT_TCP (1ULL << 1)
#define SCOPE_ABSTRACT_UNIX_SOCKET (1ULL << 0)
#define SCOPE_SIGNAL (1ULL << 1)
struct ap_ruleset_attr { uint64_t fs, net, scoped; };
struct ap_path_attr { uint64_t allowed_access; int32_t parent_fd; } __attribute__((packed));
struct ap_net_attr { uint64_t allowed_access, port; };

static int add_path(int rules, const char *path, uint64_t access) {
    int fd = open(path, O_PATH | O_CLOEXEC);
    if (fd < 0) return -1;
    struct ap_path_attr attr = {access, fd};
    int result = (int)syscall(SYS_landlock_add_rule, rules, 1, &attr, 0);
    int saved = errno;
    close(fd);
    errno = saved;
    return result;
}

static int add_port(int rules, uint16_t port, uint64_t access) {
    struct ap_net_attr attr = {access, port};
    return (int)syscall(SYS_landlock_add_rule, rules, 2, &attr, 0);
}

static int failure(int abi, const char *stage) {
    int saved = errno;
    /* Stage names are compiled constants, never application/environment data. */
    printf("{\"protocolVersion\":%d,\"abi\":%d,\"ready\":false,\"stage\":\"%s\",\"errno\":%d}\n",
           PROTOCOL_VERSION, abi > 0 ? abi : 0, stage, saved);
    return 1;
}

static int tcp_socket(void) { return socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0); }
static struct sockaddr_in addr(uint16_t port) {
    struct sockaddr_in a;
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    a.sin_port = htons(port);
    return a;
}
static int denied_errno(void) { return errno == EACCES || errno == EPERM; }

/* Returns a stable check number to the unsandboxed parent, avoiding JSON I/O
 * or cleanup requirements inside the restricted domain. */
static int check_child(int rules, const char *allowed, const char *blocked,
                       uint16_t port, pid_t parent, struct sockaddr_un *unix_addr,
                       socklen_t unix_len) {
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) return 1;
    if (syscall(SYS_landlock_restrict_self, rules, 0) < 0) return 2;
    close(rules);
    int fd = open(allowed, O_RDONLY | O_CLOEXEC);
    if (fd < 0) return 3;
    close(fd);
    fd = open(blocked, O_RDONLY | O_CLOEXEC);
    if (fd >= 0) { close(fd); return 4; }
    if (!denied_errno()) return 5;
    fd = open(allowed, O_WRONLY | O_CLOEXEC);
    if (fd >= 0) { close(fd); return 6; }
    if (!denied_errno()) return 7;

    int listener = tcp_socket();
    struct sockaddr_in a = addr(port);
    if (listener < 0 || bind(listener, (struct sockaddr *)&a, sizeof(a)) < 0 || listen(listener, 1) < 0) return 8;
    int client = tcp_socket();
    if (client < 0 || connect(client, (struct sockaddr *)&a, sizeof(a)) < 0) return 9;
    close(client);
    close(listener);

    int sock = tcp_socket();
    a = addr(0); /* An ephemeral bind is not a grant for the selected port. */
    if (sock < 0) return 10;
    if (bind(sock, (struct sockaddr *)&a, sizeof(a)) >= 0) { close(sock); return 11; }
    if (!denied_errno()) { close(sock); return 12; }
    a = addr(port == 1 ? 2 : 1);
    if (connect(sock, (struct sockaddr *)&a, sizeof(a)) >= 0) { close(sock); return 13; }
    if (!denied_errno()) { close(sock); return 14; }
    close(sock);

    /* Parent ignores this harmless signal before the test. */
    if (kill(parent, SIGUSR1) >= 0 || !denied_errno()) return 15;
    sock = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (sock < 0) return 16;
    if (connect(sock, (struct sockaddr *)unix_addr, unix_len) >= 0) { close(sock); return 17; }
    if (!denied_errno()) { close(sock); return 18; }
    close(sock);
    return 0;
}

static int probe(void) {
    int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, 1);
    if (abi < REQUIRED_ABI) return failure(abi, "landlock_abi");
    if (prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) < 0) return failure(abi, "dumpable");
    struct ap_ruleset_attr attr = {FS_HANDLED, NET_BIND_TCP | NET_CONNECT_TCP,
        SCOPE_SIGNAL | SCOPE_ABSTRACT_UNIX_SOCKET};
    int rules = (int)syscall(SYS_landlock_create_ruleset, &attr, sizeof(attr), 0);
    if (rules < 0) return failure(abi, "create_ruleset");

    char dir[] = "/tmp/ap-webapp-probe-XXXXXX";
    if (!mkdtemp(dir)) { close(rules); return failure(abi, "temporary_directory"); }
    char allowed[256], blocked[256];
    snprintf(allowed, sizeof(allowed), "%s/allowed", dir);
    snprintf(blocked, sizeof(blocked), "%s/blocked", dir);
    int fd1 = open(allowed, O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0600);
    int fd2 = open(blocked, O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0600);
    int tcp = tcp_socket(), unx = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    struct sockaddr_in a = addr(0);
    struct sockaddr_un ua;
    memset(&ua, 0, sizeof(ua));
    ua.sun_family = AF_UNIX;
    snprintf(ua.sun_path + 1, sizeof(ua.sun_path) - 1, "ap-webapp-probe-%ld-%s", (long)getpid(), dir + 21);
    socklen_t ulen = (socklen_t)(offsetof(struct sockaddr_un, sun_path) + 1 + strlen(ua.sun_path + 1));
    socklen_t alen = sizeof(a);
    const char *stage = "probe_setup";
    int result = 1;
    if (fd1 < 0 || fd2 < 0 || tcp < 0 || unx < 0) goto cleanup;
    close(fd1); fd1 = -1;
    close(fd2); fd2 = -1;
    if (bind(tcp, (struct sockaddr *)&a, sizeof(a)) < 0 || getsockname(tcp, (struct sockaddr *)&a, &alen) < 0) goto cleanup;
    if (bind(unx, (struct sockaddr *)&ua, ulen) < 0 || listen(unx, 1) < 0) goto cleanup;
    uint16_t port = ntohs(a.sin_port);
    close(tcp); tcp = -1;
    if (add_path(rules, allowed, FS_READ_FILE) < 0 || add_port(rules, port, NET_BIND_TCP | NET_CONNECT_TCP) < 0) { stage = "add_rule"; goto cleanup; }
    signal(SIGUSR1, SIG_IGN);
    pid_t parent = getpid();
    pid_t pid = fork();
    if (pid < 0) { stage = "fork"; goto cleanup; }
    if (pid == 0) {
        close(unx); /* Do not hand the parent's listening socket to the domain. */
        _exit(check_child(rules, allowed, blocked, port, parent, &ua, ulen));
    }
    int status;
    while (waitpid(pid, &status, 0) < 0) { if (errno != EINTR) { stage = "wait"; goto cleanup; } }
    if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
        int check = WIFEXITED(status) ? WEXITSTATUS(status) : -1;
        printf("{\"protocolVersion\":%d,\"abi\":%d,\"ready\":false,\"stage\":\"enforcement\",\"check\":%d}\n", PROTOCOL_VERSION, abi, check);
        result = 2;
        goto cleanup;
    }
    result = 0;
cleanup:
    { int saved = errno;
      if (fd1 >= 0) close(fd1);
      if (fd2 >= 0) close(fd2);
      if (tcp >= 0) close(tcp);
      if (unx >= 0) close(unx);
      close(rules);
      unlink(allowed); unlink(blocked); rmdir(dir);
      errno = saved; }
    if (result == 1) return failure(abi, stage);
    if (result == 2) return 1;
    printf("{\"protocolVersion\":%d,\"abi\":%d,\"ready\":true}\n", PROTOCOL_VERSION, abi);
    return 0;
}

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "--probe") == 0) return probe();
    if (argc == 2 && strcmp(argv[1], "--version") == 0) {
        puts("webapp-supervisor protocol=1");
        return 0;
    }
    fputs("usage: webapp-supervisor --probe|--version\n", stderr);
    return 64;
}
