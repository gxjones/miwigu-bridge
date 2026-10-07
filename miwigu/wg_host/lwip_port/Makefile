LWIP := ../third_party/lwip-STABLE-2_2_1_RELEASE
CC ?= gcc
OUT ?= liblwip_port.so
CFLAGS := -O2 -Wall -Wno-unused-parameter -fPIC -Iinclude -I$(LWIP)/src/include
CORE := init def dns inet_chksum ip mem memp netif pbuf raw stats sys altcp altcp_alloc altcp_tcp tcp tcp_in tcp_out timeouts udp
CORE4 := acd autoip dhcp etharp icmp igmp ip4_frag ip4 ip4_addr
SRCS := glue.c $(addprefix $(LWIP)/src/core/,$(addsuffix .c,$(CORE))) $(addprefix $(LWIP)/src/core/ipv4/,$(addsuffix .c,$(CORE4)))

$(OUT): $(SRCS)
	mkdir -p $(dir $(OUT))
	$(CC) $(CFLAGS) -shared -o $(OUT) $(SRCS)

clean:
	rm -f liblwip_port.so
