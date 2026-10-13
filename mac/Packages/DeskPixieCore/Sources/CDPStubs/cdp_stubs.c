// Stubs for the dp_* C ABI, used ONLY by `swift test` (test target depends on
// CDPStubs; the app build links the real libdeskpixie.dylib instead).
#include <stdint.h>
#include <stdlib.h>

int   dp_init(void)            { return 0; }
int   dp_configure(char* json) { (void)json; return 0; }
void  dp_hotkey(void)          {}
int   dp_push_audio(const int16_t* samples, int count, int64_t captured_unix_nanos) { (void)samples; (void)count; (void)captured_unix_nanos; return 0; }
int   dp_pull_audio(int16_t* dst, int max, int* flush) { (void)dst; (void)max; if (flush) *flush = 0; return 0; }
void  dp_report_playback(int64_t played, int buffered, float rms, int64_t underruns) { (void)played; (void)buffered; (void)rms; (void)underruns; }
void  dp_mic_failed(char* reason) { (void)reason; }
char* dp_poll_event(void)      { return NULL; }
char* dp_pack_json(void)       { return NULL; }
char* dp_last_error(void)      { return NULL; }
void  dp_free_string(char* s)  { (void)s; }
void  dp_destroy(void)         {}
