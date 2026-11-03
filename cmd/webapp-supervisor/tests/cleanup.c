#define main production_main
#include "../main.c"
#undef main

/* Native regression for setsid/double-fork and adopted descendants. Production
 * never enables child_process; this harness exercises the C cleanup boundary. */
int main(void) {
    pid_t sentinel = fork();
    if (sentinel < 0) return 1;
    if (sentinel == 0) { for (;;) pause(); }
    pid_t watcher = fork();
    if (watcher < 0) return 2;
    if (watcher == 0) {
        if (prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) < 0) _exit(3);
        int ready[2]; if (pipe2(ready, O_CLOEXEC) < 0) _exit(4);
        pid_t leader = fork();
        if (leader == 0) {
            close(ready[0]); signal(SIGTERM, SIG_IGN);
            if (setsid() < 0) _exit(5);
            pid_t intermediate = fork();
            if (intermediate == 0) {
                if (setsid() < 0) _exit(6);
                pid_t daemon = fork();
                if (daemon == 0) { char byte = 1; write(ready[1], &byte, 1); close(ready[1]); for (;;) pause(); }
                _exit(0);
            }
            close(ready[1]); for (;;) pause();
        }
        close(ready[1]); char byte; if (read(ready[0], &byte, 1) != 1) _exit(7); close(ready[0]);
        stopping = 1; int status = 0;
        if (watch_tree(leader, 100, 0, &status) != 0) _exit(8);
        if (waitpid(-1, NULL, WNOHANG) != -1 || errno != ECHILD) _exit(9);
        _exit(0);
    }
    int status;
    if (waitpid(watcher, &status, 0) != watcher || !WIFEXITED(status) || WEXITSTATUS(status) != 0) { kill(sentinel, SIGKILL); return 10; }
    // The sibling belongs to the test's parent, outside the watcher's tree.
    if (kill(sentinel, 0) != 0) return 11;
    kill(sentinel, SIGKILL); waitpid(sentinel, NULL, 0);
    puts("setsid/double-fork descendants reaped; unrelated sibling retained");
    return 0;
}
