.PHONY: host test firmware hardware-check repo-check

host:
	$(MAKE) -C host build

test:
	$(MAKE) -C host test
	$(MAKE) -C firmware test

firmware:
	$(MAKE) -C firmware

hardware-check:
	$(MAKE) -C hardware check

repo-check:
	git diff --check --cached
	$(MAKE) hardware-check
