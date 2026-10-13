// CDP: hand-written copy of the libdeskpixie C ABI (core/cmd/libdeskpixie).
// Linkage against libdeskpixie.dylib is configured by the Xcode app target.
#ifndef CDP_LIBDESKPIXIE_SHIM_H
#define CDP_LIBDESKPIXIE_SHIM_H

#include <stdint.h>
#include <stdlib.h>

int   dp_init(void);
int   dp_configure(char* json);
void  dp_hotkey(void);
int   dp_push_audio(const int16_t* samples, int count, int64_t captured_unix_nanos);
int   dp_pull_audio(int16_t* dst, int max, int* flush);
void  dp_report_playback(int64_t played, int buffered, float rms, int64_t underruns);
void  dp_mic_failed(char* reason);
char* dp_poll_event(void);
char* dp_pack_json(void);
char* dp_last_error(void);
void  dp_free_string(char* s);
void  dp_destroy(void);

// Lock-free atomic helpers over plain int64 storage (acquire/release).
static inline int64_t cdp_load(const int64_t* p) { return __atomic_load_n(p, __ATOMIC_ACQUIRE); }
static inline void cdp_store(int64_t* p, int64_t v) { __atomic_store_n(p, v, __ATOMIC_RELEASE); }
static inline int64_t cdp_add(int64_t* p, int64_t v) { return __atomic_add_fetch(p, v, __ATOMIC_ACQ_REL); }

#endif
