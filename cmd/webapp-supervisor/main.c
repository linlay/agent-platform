/* Linux-only, unprivileged WebApp supervisor and sandbox probe.
 * The Go executable remains CGO_ENABLED=0.
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

#define PROTOCOL_VERSION 3
#define REQUIRED_ABI 4
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
                       socklen_t unix_len, int ipc_scoped) {
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

    if (!ipc_scoped) return 0;
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
    int ipc_scoped = abi >= 6;
    int ioctl_restricted = abi >= 5;
    struct ap_ruleset_attr attr = {
        ioctl_restricted ? FS_HANDLED : FS_HANDLED & ~(1ULL << 15),
        NET_BIND_TCP | NET_CONNECT_TCP,
        ipc_scoped ? SCOPE_SIGNAL | SCOPE_ABSTRACT_UNIX_SOCKET : 0
    };
    size_t attr_size = ipc_scoped ? sizeof(attr) : offsetof(struct ap_ruleset_attr, scoped);
    int rules = (int)syscall(SYS_landlock_create_ruleset, &attr, attr_size, 0);
    if (rules < 0) return failure(abi, "create_ruleset");

    char dir[] = "/tmp/ap-webapp-probe-XXXXXX";
    if (!mkdtemp(dir)) { close(rules); return failure(abi, "temporary_directory"); }
    char allowed[256], blocked[256];
    snprintf(allowed, sizeof(allowed), "%s/allowed", dir);
    snprintf(blocked, sizeof(blocked), "%s/blocked", dir);
    int fd1 = open(allowed, O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0600);
    int fd2 = open(blocked, O_CREAT | O_EXCL | O_WRONLY | O_CLOEXEC, 0600);
    int tcp = tcp_socket(), unx = -1;
    if (ipc_scoped) unx = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    struct sockaddr_in a = addr(0);
    struct sockaddr_un ua;
    memset(&ua, 0, sizeof(ua));
    ua.sun_family = AF_UNIX;
    snprintf(ua.sun_path + 1, sizeof(ua.sun_path) - 1, "ap-webapp-probe-%ld-%s", (long)getpid(), dir + 21);
    socklen_t ulen = (socklen_t)(offsetof(struct sockaddr_un, sun_path) + 1 + strlen(ua.sun_path + 1));
    socklen_t alen = sizeof(a);
    const char *stage = "probe_setup";
    int result = 1;
    if (fd1 < 0 || fd2 < 0 || tcp < 0 || (ipc_scoped && unx < 0)) goto cleanup;
    close(fd1); fd1 = -1;
    close(fd2); fd2 = -1;
    if (bind(tcp, (struct sockaddr *)&a, sizeof(a)) < 0 || getsockname(tcp, (struct sockaddr *)&a, &alen) < 0) goto cleanup;
    if (ipc_scoped && (bind(unx, (struct sockaddr *)&ua, ulen) < 0 || listen(unx, 1) < 0)) goto cleanup;
    uint16_t port = ntohs(a.sin_port);
    close(tcp); tcp = -1;
    if (add_path(rules, allowed, FS_READ_FILE) < 0 || add_port(rules, port, NET_BIND_TCP | NET_CONNECT_TCP) < 0) { stage = "add_rule"; goto cleanup; }
    signal(SIGUSR1, SIG_IGN);
    pid_t parent = getpid();
    pid_t pid = fork();
    if (pid < 0) { stage = "fork"; goto cleanup; }
    if (pid == 0) {
        if (unx >= 0) close(unx); /* No parent listener is inherited. */
        _exit(check_child(rules, allowed, blocked, port, parent, &ua, ulen, ipc_scoped));
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
    printf("{\"protocolVersion\":%d,\"abi\":%d,\"ready\":true,\"ipcScoped\":%s,\"deviceIoctlRestricted\":%s}\n",
           PROTOCOL_VERSION, abi, ipc_scoped ? "true" : "false", ioctl_restricted ? "true" : "false");
    return 0;
}

/* --run uses FD 3 (parent-liveness pipe), FD 4 (private status pipe), and
 * FD 5 (exclusive generation lock). None survive exec into application Node.
 * A watcher and a subreaper supervise one another: killing either one leaves
 * the other able to reap the Node tree. Container init is the final boundary.
 */
#include <dirent.h>
#include <limits.h>
#include <poll.h>
#include <sys/file.h>
#include <time.h>

static volatile sig_atomic_t stopping;
static void on_stop(int signal_number) { (void)signal_number; stopping = 1; }
static int64_t monotonic_ms(void) {
    struct timespec t; if (clock_gettime(CLOCK_MONOTONIC, &t) < 0) return 0;
    return (int64_t)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}
static int number(const char *s, int min, int max) {
    char *end; errno = 0; long n = strtol(s, &end, 10);
    if (errno || !*s || *end || n < min || n > max) return -1;
    return (int)n;
}
static void status_error(const char *stage) {
    dprintf(4, "{\"event\":\"error\",\"stage\":\"%s\"}\n", stage);
    dprintf(2,"webapp-supervisor stage=%s errno=%d\n",stage,errno);
}
static void signal_tree(pid_t parent, int sig, int depth) {
    (void)depth;
    char path[96]; snprintf(path, sizeof(path), "/proc/%ld/task/%ld/children", (long)parent, (long)parent);
    FILE *file = fopen(path, "re"); if (!file) return;
    long child;
    while (fscanf(file, "%ld", &child) == 1) {
        if (child <= 1 || child == getpid()) continue;
        int fd = (int)syscall(SYS_pidfd_open, (pid_t)child, 0);
        if (fd < 0) continue;
        // Verify current parent after obtaining the stable process handle.
        // Repeatedly killing direct children makes setsid/double-fork orphans
        // become our children through subreaper adoption, without PID races.
        snprintf(path, sizeof(path), "/proc/%ld/status", child);
        FILE *info = fopen(path, "re"); long ppid = -1; char line[256];
        if (info) { while (fgets(line, sizeof(line), info)) if (sscanf(line, "PPid: %ld", &ppid) == 1) break; fclose(info); }
        if (ppid == (long)parent) (void)syscall(SYS_pidfd_send_signal, fd, sig, NULL, 0);
        close(fd);
    }
    fclose(file);
}
static int collect_children(pid_t leader, int *leader_status, int *leader_exited) {
    for (;;) {
        int status; pid_t child = waitpid(-1, &status, WNOHANG);
        if (child > 0) { if (child == leader) { *leader_exited = 1; *leader_status = status; } continue; }
        if (child == 0) return 0;
        if (errno == EINTR) continue;
        return errno == ECHILD ? 1 : -1;
    }
}
static int watch_tree(pid_t leader, int grace, int watch_control, int *status) {
    int exited = 0, terminating = 0; int64_t deadline = 0;
    for (;;) {
        int empty = collect_children(leader, status, &exited);
        if (empty == 1) return 0;
        if (empty < 0) return -1;
        struct pollfd control = {3, POLLIN | POLLHUP | POLLERR, 0};
        int result = poll(watch_control ? &control : NULL, watch_control ? 1 : 0, 50);
        if (watch_control && result > 0 && control.revents) stopping = 1;
        if ((stopping || exited) && !terminating) { terminating = 1; deadline = monotonic_ms() + grace; }
        if (terminating) {
            int sig = monotonic_ms() >= deadline ? SIGKILL : SIGTERM;
            signal_tree(getpid(), sig, 0);
            if (!exited) (void)kill(-leader, sig);
            // Never claim clean exit while a child is still alive. The outer
            // watcher also performs this drain if its inner supervisor dies.
        }
    }
}
static int optional_system_path(int rules, const char *path, uint64_t access) {
    struct stat st;
    if (stat(path, &st) < 0) return errno == ENOENT ? 0 : -1;
    if (st.st_uid != 0 || (st.st_mode & 0022)) { errno = EPERM; return -1; }
    return add_path(rules, path, access);
}
static int restrict_node(char **argv) {
    int abi = (int)syscall(SYS_landlock_create_ruleset, NULL, 0, 1);
    if (abi < REQUIRED_ABI) return -1;
    uint64_t handled = abi >= 5 ? FS_HANDLED : FS_HANDLED & ~(1ULL << 15);
    struct ap_ruleset_attr attr = {handled, NET_BIND_TCP | NET_CONNECT_TCP,
        abi >= 6 ? SCOPE_SIGNAL | SCOPE_ABSTRACT_UNIX_SOCKET : 0};
    int rules = (int)syscall(SYS_landlock_create_ruleset, &attr,
        abi >= 6 ? sizeof(attr) : offsetof(struct ap_ruleset_attr, scoped), 0);
    if (rules < 0) return -1;
    uint64_t read = FS_READ_FILE | FS_READ_DIR;
    uint64_t write = read | (1ULL << 1) | (1ULL << 4) | (1ULL << 5) |
        (1ULL << 7) | (1ULL << 8) | (1ULL << 9) | (1ULL << 10) |
        (1ULL << 12) | (1ULL << 13) | (1ULL << 14);
    int result = -1;
    if (add_path(rules, argv[2], read) < 0 || add_path(rules, argv[3], write) < 0 ||
        add_path(rules, argv[4], write) < 0 || add_path(rules, argv[5], FS_READ_FILE) < 0 ||
        add_path(rules, "/opt/node/bin/node", FS_READ_FILE | 1ULL) < 0) goto done;
    const char *dirs[] = {"/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu", "/usr/share/zoneinfo", NULL};
    const char *files[] = {"/etc/ld.so.cache", "/etc/ssl/openssl.cnf", "/etc/ssl/certs/ca-certificates.crt", "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/localtime", NULL};
    for (int i=0; dirs[i]; i++) if (optional_system_path(rules, dirs[i], read) < 0) goto done;
    for (int i=0; files[i]; i++) if (optional_system_path(rules, files[i], FS_READ_FILE) < 0) goto done;
    if (optional_system_path(rules, "/lib64/ld-linux-x86-64.so.2", FS_READ_FILE | 1ULL) < 0) goto done;
    if (add_path(rules, "/dev/urandom", FS_READ_FILE) < 0) goto done;
    if (add_path(rules, "/dev/null", FS_READ_FILE | (1ULL << 1)) < 0) goto done;
    int port = number(argv[6], 1024, 65535), bridge = number(argv[7], 0, 65535);
    if (port < 0 || bridge < 0 || port == bridge || add_port(rules, (uint16_t)port, NET_BIND_TCP) < 0 || (bridge && add_port(rules, (uint16_t)bridge, NET_CONNECT_TCP) < 0)) goto done;
    char *extra = strdup(argv[8]); if (!extra) goto done;
    char *save = NULL, *part = strtok_r(extra, ",", &save);
    while (part) { int n = number(part, 1, 65535); if (n < 0 || add_port(rules, (uint16_t)n, NET_CONNECT_TCP) < 0) { free(extra); goto done; } part = strtok_r(NULL, ",", &save); }
    free(extra);
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0 || syscall(SYS_landlock_restrict_self, rules, 0) < 0) goto done;
    result = 0;
done: { int saved = errno; close(rules); errno = saved; } return result;
}
static int exit_code(int status) {
    return WIFEXITED(status) ? WEXITSTATUS(status) : WIFSIGNALED(status) ? 128 + WTERMSIG(status) : 70;
}
static int inner_run(char **argv, int grace) {
    pid_t parent = getppid();
    if (prctl(PR_SET_PDEATHSIG, SIGTERM, 0, 0, 0) < 0 || getppid() != parent ||
        prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) < 0) return 70;
    int errors[2]; if (pipe2(errors, O_CLOEXEC) < 0) return 70;
    pid_t node = fork(); if (node < 0) return 70;
    if (node == 0) {
        close(errors[0]); pid_t supervisor = getppid();
        int failed = prctl(PR_SET_PDEATHSIG, SIGKILL, 0, 0, 0) < 0 || getppid() != supervisor ||
            setsid() < 0 || chdir(argv[2]) < 0 || restrict_node(argv) < 0;
        if (!failed) {
            // An exec-error pipe is the only temporary non-stdio descriptor;
            // CLOEXEC closes it before any application code runs.
            if (dup3(errors[1], 3, O_CLOEXEC) < 0 || syscall(SYS_close_range, 4U, ~0U, 0) < 0) failed = 1;
            else { execv("/opt/node/bin/node", &argv[11]); failed = 1; }
        }
        if (failed) { int value = errno ? errno : EACCES; (void)write(errors[1], &value, sizeof(value)); (void)write(3, &value, sizeof(value)); }
        _exit(77);
    }
    close(errors[1]); int error = 0; ssize_t count;
    do { count = read(errors[0], &error, sizeof(error)); } while (count < 0 && errno == EINTR);
    close(errors[0]);
    if (count != 0) { errno = error; status_error("isolation_or_exec"); stopping = 1; }
    else dprintf(4, "{\"event\":\"started\",\"pid\":%ld}\n", (long)node);
    int status = 0;
    if (watch_tree(node, grace, 1, &status) < 0) return 86;
    return count == 0 ? exit_code(status) : 77;
}
static int run_application(int argc, char **argv) {
    if (argc < 12 || strcmp(argv[10], "--") || strcmp(argv[11], "/opt/node/bin/node") ||
        getuid() == 0 || getuid() != geteuid()) return 64;
    int grace = number(argv[9], 1, 30000); if (grace < 0) return 64;
    for (int i = 2; i <= 5; i++) { char resolved[PATH_MAX]; if (!realpath(argv[i], resolved) || strcmp(resolved, argv[i])) return 64; }
    struct stat control, status;
    if (fstat(3, &control) < 0 || fstat(4, &status) < 0 || !S_ISFIFO(control.st_mode) || !S_ISFIFO(status.st_mode) ||
        prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) < 0 || prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) < 0 ||
        prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0 || flock(5, LOCK_EX | LOCK_NB) < 0) { status_error("supervisor_setup"); return 77; }
    signal(SIGTERM, on_stop); signal(SIGINT, on_stop); signal(SIGPIPE, SIG_IGN);
    pid_t parent = getppid();
    if (prctl(PR_SET_PDEATHSIG, SIGTERM, 0, 0, 0) < 0 || getppid() != parent) return 77;
    pid_t inner = fork(); if (inner < 0) return 70;
    if (inner == 0) _exit(inner_run(argv, grace));
    int child_status = 0;
    if (watch_tree(inner, grace + 1000, 1, &child_status) < 0) return 86;
    int code = exit_code(child_status);
    dprintf(4, "{\"event\":\"exited\",\"code\":%d,\"clean\":true}\n", code);
    return code;
}

int main(int argc, char **argv) {
    if (argc >= 2 && strcmp(argv[1], "--run") == 0) return run_application(argc, argv);
    if (argc == 2 && strcmp(argv[1], "--probe") == 0) return probe();
    if (argc == 2 && strcmp(argv[1], "--version") == 0) {
        puts("webapp-supervisor protocol=3");
        return 0;
    }
    fputs("usage: webapp-supervisor --probe|--version\n", stderr);
    return 64;
}
