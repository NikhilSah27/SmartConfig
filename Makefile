BIN := bin/sc
export CGO_ENABLED := 0

.PHONY: build deb test race vet fmt smoke m1-compat accept-m2 accept-m3 accept-m4 clean lab-e2e lab-image lab-test lab-clean

# The QEMU rescue lab (lab/README.md): dev only, stdlib Python, no sudo, no KVM, not in CI.
LAB = env PYTHONDONTWRITEBYTECODE=1 python3
LAB_MODES ?= uefi bios
LAB_TIMEOUT ?= 4500
LAB_E2E_ARGS ?=
LAB_IMAGE_FROM ?=
LAB_FORCE ?=

# The package version (M5 plan 2): 0.N.0 at the tag mN, else
# 0.N.99+git<count>.<commit time>.<sha7> after mN (scripts/version.sh;
# count is the commits since mN, M5 plan C3); from the commit, so the same
# tree builds the same bytes (the lab's P.2 builds it again to compare).
VERSION ?= $(shell sh scripts/version.sh 2>/dev/null || echo devel)

build:
	go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/sc

# The package (M5): dist/smartconfig_<VERSION>_amd64.deb, dpkg-deb only;
# the same commit builds the same bytes.
deb: build
	sh scripts/build-deb.sh dist/smartconfig_$(VERSION)_amd64.deb

test:
	go test ./...

race:
	CGO_ENABLED=1 go test -race ./...

vet:
	go vet ./...

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

smoke: build
	sudo ./scripts/smoke.sh

# M2 acceptance in the VM (plan section 12): touches only its own test paths.
accept-m2: build
	sudo ./scripts/accept-m2.sh

accept-m3: build
	sudo ./scripts/accept-m3.sh

# M4 acceptance in the VM, the parts that need no reboot (plan step 12, S1):
# nothing real is written.
accept-m4: build
	sudo ./scripts/accept-m4.sh

# The M1 binary must keep working on a store the M2 code has migrated.
m1-compat:
	./scripts/build-sc-m1.sh bin/sc-m1
	SC_M1_BIN=$(CURDIR)/bin/sc-m1 go test -count=1 -run TestM1Compat ./internal/store

clean:
	rm -rf bin dist

# The M4 owner scenario in a VM, every mode in LAB_MODES (about 25 min each
# under TCG); needs make lab-image once. The recipe ends in exit 1 if a mode
# FAILed (an [M4] check), else 3 if one was INCONCLUSIVE (a [lab] problem,
# a timeout too); make shows it as "Error 1" or "Error 3" and exits 2. Each
# mode's summary line is echoed; the last line says boot5=no when boot 5 was
# left out (--no-boot5), so such a PASS never reads as a whole one, and
# grubpw=yes when --grub-password ran the README's GRUB password recipe too.
lab-e2e: build
	@rc=0; tags="$(if $(findstring --no-boot5,$(LAB_E2E_ARGS)), boot5=no)$(if $(findstring --grub-password,$(LAB_E2E_ARGS)), grubpw=yes)$(if $(findstring --deb,$(LAB_E2E_ARGS)), deb=yes)"; for m in $(LAB_MODES); do \
	  line=$$(timeout --foreground $(LAB_TIMEOUT) $(LAB) lab/e2e.py --mode $$m $(LAB_E2E_ARGS)); r=$$?; \
	  [ -n "$$line" ] && echo "$$line"; \
	  for t in boot5=no dirty=yes "boot2=a(forced)"; do \
	    case " $$line " in *" $$t "*) case "$$tags " in *" $$t "*) ;; *) tags="$$tags $$t";; esac;; esac; done; \
	  case "$$r:$$line" in 0:*) ;; 1:FAIL\ *) rc=1;; *) [ $$rc = 0 ] && rc=3;; esac; done; \
	  case $$rc in 0) echo "lab-e2e: PASS$$tags";; 1) echo "lab-e2e: FAIL$$tags";; *) echo "lab-e2e: INCONCLUSIVE$$tags";; esac; \
	  exit $$rc

# The pinned cloud image (LAB_IMAGE_FROM=FILE copies it) and the reference
# image built from it (about 30 min under TCG), in the lab cache. After an
# ovmf update, P.5 asks for it again: provision --force builds it under the
# new firmware.
lab-image:
	$(LAB) lab/vm.py image $(if $(LAB_IMAGE_FROM),--from $(LAB_IMAGE_FROM)) && $(LAB) lab/vm.py provision $(if $(LAB_FORCE),--force)

lab-test:
	$(LAB) -m unittest discover -s lab
	@for f in lab/guest/*.sh lab/guest/41_sclab lab/guest/43_sclab; do sh -n "$$f" || exit 1; done

lab-clean:
	$(LAB) lab/vm.py gc
