#ifndef LWIP_ARCH_CC_H
#define LWIP_ARCH_CC_H

#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/time.h>

#define LWIP_ERRNO_STDINCLUDE 1
#define LWIP_TIMEVAL_PRIVATE 0
#define BYTE_ORDER LITTLE_ENDIAN

typedef uint8_t u8_t;
typedef int8_t s8_t;
typedef uint16_t u16_t;
typedef int16_t s16_t;
typedef uint32_t u32_t;
typedef int32_t s32_t;
typedef uintptr_t mem_ptr_t;

#define PACK_STRUCT_FIELD(x) x
#define PACK_STRUCT_STRUCT __attribute__((packed))
#define PACK_STRUCT_BEGIN
#define PACK_STRUCT_END

#define LWIP_PLATFORM_DIAG(x) do { printf x; } while (0)
#define LWIP_PLATFORM_ASSERT(x) do { fprintf(stderr, "lwip assert: %s\n", (x)); abort(); } while (0)
#define LWIP_RAND() ((u32_t)rand())

#endif
