// Stubs for the dp_* C ABI, used ONLY by `swift test` (test target depends on
// CDPStubs; the app build links the real libdeskpixie.dylib instead).
#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>

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
// dp_call stub: "stub.echo" answers {"ok":<args>}, "stub.fail" {"error":"stub failure"},
// anything else {"error":"unknown method <m>"} (the Go core's answer for an unknown method).
// Strings are malloc'd; dp_free_string is a no-op here, so tests leak a few bytes.
char* dp_call(char* method, char* args_json) {
    const char* m = method ? method : "";
    const char* a = (args_json && *args_json) ? args_json : "{}";
    size_t n = strlen(m) + strlen(a) + 64;
    char* out = malloc(n);
    if (!out) return NULL;
    if (strcmp(m, "stub.echo") == 0) snprintf(out, n, "{\"ok\":%s}", a);
    else if (strcmp(m, "stub.fail") == 0) snprintf(out, n, "{\"error\":\"stub failure\"}");
    else snprintf(out, n, "{\"error\":\"unknown method %s\"}", m);
    return out;
}
void  dp_free_string(char* s)  { (void)s; }
void  dp_destroy(void)         {}
