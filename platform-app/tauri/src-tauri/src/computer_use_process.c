#include "computer_use_process.h"
#include <libproc.h>

bool ga_process_identity_read(int32_t pid, GAProcessIdentity *identity) {
    if (!identity) return false;
    *identity = (GAProcessIdentity){0};
    if (pid <= 0) return false;
    struct proc_bsdinfo info = {0};
    int size = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
    if (size != sizeof(info) || info.pbi_pid != (uint32_t)pid ||
        (info.pbi_flags & PROC_FLAG_INEXIT) || info.pbi_start_tvsec == 0 ||
        info.pbi_start_tvusec >= 1000000) return false;
    *identity = (GAProcessIdentity){
        .pid = info.pbi_pid,
        .start_seconds = info.pbi_start_tvsec,
        .start_microseconds = info.pbi_start_tvusec,
    };
    return true;
}
