# Builds the miwigu outputs:
#
#   make wstoudp    local proxy (WireGuard UDP <-> WebSocket) for every OS, in bin/
#   make libs       BoringTun and lwIP shared libraries in miwigu/wg_host/dist/
#   make artifact   app.zip, the guest Docker build context that scripts/02 uploads
#   make check      vet, test, and syntax-check the Go, Rust, Python, and shell code
#
# The native libraries are Linux shared objects, so `make libs` needs a Linux
# toolchain: gcc for x86_64 and aarch64-linux-gnu-gcc for the Graviton guest.
# Override the compilers on the command line, e.g. make AARCH64_CC=clang.

WG_HOST := miwigu/wg_host
WSTOUDP := miwigu/wstoudp
DIST    := $(WG_HOST)/dist
BIN     := bin

GUEST_TARGET := aarch64-unknown-linux-gnu
HOST_TARGET  := x86_64-unknown-linux-gnu

AARCH64_CC ?= aarch64-linux-gnu-gcc
AARCH64_AR ?= aarch64-linux-gnu-ar
X86_64_CC  ?= gcc
X86_64_AR  ?= ar
PYTHON     ?= python3

.PHONY: all wstoudp libs guest-libs host-libs artifact check clean

all: wstoudp libs artifact

# --- wstoudp ---------------------------------------------------------------

WSTOUDP_SRCS := $(wildcard $(WSTOUDP)/*.go) $(WSTOUDP)/go.mod $(WSTOUDP)/go.sum
WSTOUDP_PLATFORMS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64
WSTOUDP_BINS := $(foreach p,$(WSTOUDP_PLATFORMS),$(BIN)/$(p)/wstoudp) \
	$(BIN)/windows-amd64/wstoudp.exe $(BIN)/windows-amd64/wstoudp.exe.manifest

wstoudp: $(WSTOUDP_BINS)

$(BIN)/%/wstoudp: $(WSTOUDP_SRCS)
	cd $(WSTOUDP) && CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) \
		go build -trimpath -o $(abspath $@) .

# The Walk GUI loads common controls v6 from the side-by-side manifest.
$(BIN)/windows-amd64/wstoudp.exe: $(WSTOUDP_SRCS)
	cd $(WSTOUDP) && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -trimpath -ldflags="-H windowsgui" -o $(abspath $@) .

$(BIN)/windows-amd64/wstoudp.exe.manifest: $(WSTOUDP)/wstoudp.exe.manifest
	mkdir -p $(@D)
	cp $< $@

# --- guest native libraries ------------------------------------------------

LWIP_SRCS := $(WG_HOST)/lwip_port/Makefile $(WG_HOST)/lwip_port/glue.c \
	$(wildcard $(WG_HOST)/lwip_port/include/*.h $(WG_HOST)/lwip_port/include/arch/*.h)
TUNN_SRCS := $(wildcard $(WG_HOST)/tunn_ffi/src/*.rs) \
	$(WG_HOST)/tunn_ffi/Cargo.toml $(WG_HOST)/tunn_ffi/Cargo.lock

CC_$(GUEST_TARGET) := $(AARCH64_CC)
CC_$(HOST_TARGET)  := $(X86_64_CC)
CARGO_ENV_$(GUEST_TARGET) := CC_aarch64_unknown_linux_gnu=$(AARCH64_CC) \
	AR_aarch64_unknown_linux_gnu=$(AARCH64_AR) \
	CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=$(AARCH64_CC)
CARGO_ENV_$(HOST_TARGET) := CC_x86_64_unknown_linux_gnu=$(X86_64_CC) \
	AR_x86_64_unknown_linux_gnu=$(X86_64_AR) \
	CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER=$(X86_64_CC)

guest-libs: $(DIST)/$(GUEST_TARGET)/liblwip_port.so $(DIST)/$(GUEST_TARGET)/libtunn_ffi.so
host-libs:  $(DIST)/$(HOST_TARGET)/liblwip_port.so $(DIST)/$(HOST_TARGET)/libtunn_ffi.so
libs: guest-libs host-libs

$(DIST)/%/liblwip_port.so: $(LWIP_SRCS)
	$(MAKE) -C $(WG_HOST)/lwip_port OUT=$(abspath $@) CC=$(CC_$*)

$(DIST)/%/libtunn_ffi.so: $(TUNN_SRCS)
	cd $(WG_HOST)/tunn_ffi && $(CARGO_ENV_$*) cargo build --release --locked --target $*
	mkdir -p $(@D)
	cp $(WG_HOST)/tunn_ffi/target/$*/release/libtunn_ffi.so $@

# --- MicroVM artifact ------------------------------------------------------

# Keep in sync with FILES_TO_ZIP in scripts/02_upload_artifact.py.
ARTIFACT_FILES := Dockerfile wg_host_listener.py success_server.py ssh_server.py start.sh \
	install-guacd.sh patches/guacamole-2328-1.6.0.patch \
	guac/package.json guac/package-lock.json guac/server.js \
	dist/$(GUEST_TARGET)/libtunn_ffi.so dist/$(GUEST_TARGET)/liblwip_port.so

artifact: app.zip

app.zip: $(addprefix $(WG_HOST)/,$(ARTIFACT_FILES))
	rm -f $@
	cd $(WG_HOST) && zip -q -X $(abspath $@) $(ARTIFACT_FILES)

# --- checks ----------------------------------------------------------------

PY_SRCS := config.py $(wildcard scripts/*.py) $(wildcard $(WG_HOST)/*.py) $(wildcard local/*.py)
SH_SRCS := $(wildcard $(WG_HOST)/*.sh) $(wildcard local/*.sh) $(wildcard tools/*/*.sh)

check:
	cd $(WSTOUDP) && go vet ./...
	cd $(WSTOUDP) && GOOS=windows go vet ./...
	cd $(WG_HOST)/tunn_ffi && cargo test --locked
	$(PYTHON) -c 'import ast, sys; [ast.parse(open(f).read(), f) for f in sys.argv[1:]]' $(PY_SRCS)
	for f in $(SH_SRCS); do sh -n "$$f" || exit 1; done

clean:
	rm -rf $(BIN) app.zip $(WG_HOST)/tunn_ffi/target
	$(MAKE) -C $(WG_HOST)/lwip_port clean
