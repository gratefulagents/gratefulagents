#ifndef GA_TEST_LIBPROC_H
#define GA_TEST_LIBPROC_H

#ifdef __APPLE__
#include_next <libproc.h>
#else
#include <stdint.h>

// Linux fault-injection fixture, not a declaration of the Darwin ABI.
struct proc_bsdinfo {
    uint32_t pbi_flags;
    uint32_t pbi_pid;
    uint64_t pbi_start_tvsec;
    uint64_t pbi_start_tvusec;
};
#define PROC_PIDTBSDINFO 3
#define PROC_FLAG_INEXIT 4
int proc_pidinfo(int pid, int flavor, uint64_t arg, void *buffer, int buffersize);
#endif

#endif
