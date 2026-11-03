#include "quickjs.h"
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

/* No quickjs-libc, module loader, filesystem, sockets, shell or environment API.
 * The player program can only transform strings inside this isolated process. */
static uint8_t *read_frame(size_t limit, size_t *length) {
    uint8_t header[4];
    if (fread(header, 1, 4, stdin) != 4) return NULL;
    size_t n = (size_t)header[0] | ((size_t)header[1] << 8) |
               ((size_t)header[2] << 16) | ((size_t)header[3] << 24);
    if (n > limit) return NULL;
    uint8_t *data = malloc(n + 1);
    if (!data || fread(data, 1, n, stdin) != n) { free(data); return NULL; }
    data[n] = 0; *length = n; return data;
}
static int interrupt(JSContext *ctx, void *opaque) {
    (void)ctx;
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    return now.tv_sec >= *(time_t *)opaque;
}
int main(void) {
    size_t script_len, input_len;
    uint8_t *script = read_frame(512 * 1024, &script_len);
    uint8_t *input = read_frame(12 * 1024 * 1024, &input_len);
    if (!script || !input) { free(script); free(input); return 2; }
    JSRuntime *rt = JS_NewRuntime();
    if (!rt) return 3;
    JS_SetMemoryLimit(rt, 128 * 1024 * 1024);
    JS_SetGCThreshold(rt, 16 * 1024 * 1024);
    JS_SetMaxStackSize(rt, 1024 * 1024);
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    time_t deadline = now.tv_sec + 25;
    JS_SetInterruptHandler(rt, interrupt, &deadline);
    JSContext *ctx = JS_NewContext(rt);
    if (!ctx) { JS_FreeRuntime(rt); return 3; }
    JSValue global = JS_GetGlobalObject(ctx);
    JS_SetPropertyStr(ctx, global, "inputJSON", JS_NewStringLen(ctx, (char *)input, input_len));
    free(input);
    JSValue result = JS_Eval(ctx, (char *)script, script_len, "ytm-resolver", JS_EVAL_TYPE_GLOBAL);
    free(script);
    int status = 4;
    if (JS_IsException(result)) { JSValue exception=JS_GetException(ctx); JS_FreeValue(ctx,exception); }
    if (!JS_IsException(result)) {
        size_t length;
        const char *json = JS_ToCStringLen(ctx, &length, result);
        if (json && length <= 12 * 1024 * 1024 && fwrite(json, 1, length, stdout) == length) status = 0;
        JS_FreeCString(ctx, json);
    }
    /* Never print exceptions: a JS exception can contain a signed URL. */
    JS_FreeValue(ctx, result);
    JS_FreeValue(ctx, global);
    JS_FreeContext(ctx);
    JS_FreeRuntime(rt);
    return status;
}
