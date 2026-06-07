ARCH ?= $(shell uname -m)
BPF_ARCH := $(ARCH)
BPF_ARCH := $(subst x86_64,x86,$(BPF_ARCH))
BPF_ARCH := $(subst aarch64,arm64,$(BPF_ARCH))
CLANG ?= clang
BPFTOOL ?= bpftool
CFLAGS ?= -O2 -g -Wall -Werror

TARGET := tcp_metrics.bpf.o
VMLINUX := vmlinux.h

.PHONY: all clean

all: $(TARGET)

$(VMLINUX):
	$(BPFTOOL) btf dump file /sys/kernel/btf/vmlinux format c > $(VMLINUX)

$(TARGET): tcp_metrics.bpf.c $(VMLINUX)
	$(CLANG) $(CFLAGS) -target bpf -D__TARGET_ARCH_$(BPF_ARCH) -I. -c $< -o $@

clean:
	rm -f $(TARGET) $(VMLINUX)
