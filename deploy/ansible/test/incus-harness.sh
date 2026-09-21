#!/usr/bin/env bash
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only

# A throwaway Debian container to apply the invctl role to, and the one command
# that destroys it.
#
# THE NAME IS A CONSTANT AND NOT AN ARGUMENT. This host runs containers
# belonging to unrelated work, and the way unrelated work gets destroyed is a
# cleanup pattern that matches more than it meant to -- `--all`, a loop over
# `incus list`, a grep. Teardown here names one container, literally, and can
# therefore delete nothing else however badly it is invoked.
#
# incus needs sudo on this host: the socket is not readable by this user. That
# is deliberate and is not to be "fixed" by adding anybody to a group.

set -euo pipefail

CONTAINER=invctl-ansible-test
IMAGE=images:debian/13
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANSIBLE_DIR="$(dirname "$HERE")"

usage() {
	cat <<'USAGE'
usage: incus-harness.sh up | down | run [ansible-playbook args...]

  up    create invctl-ansible-test and make it usable by Ansible
  down  delete invctl-ansible-test, and only it. Safe to run twice.
  run   sudo ansible-playbook against invctl-ansible-test

Always finish with `down`. If a test aborted, run `down` by hand: a container
left behind makes the next `up` collide on the name.
USAGE
}

up() {
	if sudo incus info "$CONTAINER" >/dev/null 2>&1; then
		echo "$CONTAINER already exists; run 'down' first" >&2
		exit 1
	fi
	sudo incus launch "$IMAGE" "$CONTAINER"
	# systemd has to be up before systemctl means anything inside. --wait
	# returns non-zero for "degraded", which a minimal container often is and
	# which is not a reason to stop.
	sudo incus exec "$CONTAINER" -- systemctl is-system-running --wait || true
	# Ansible modules are Python. The image may or may not carry python3; this
	# is idempotent either way, and it is the ONLY thing the container needs
	# from the internet in the offline (files) scenario.
	sudo incus exec "$CONTAINER" -- sh -c \
		'apt-get update -qq && apt-get install -y -qq python3 ca-certificates'
	echo "$CONTAINER is ready"
}

down() {
	# --force stops it first. Missing is success: teardown runs after a failure,
	# and a teardown that fails because there was nothing to tear down turns a
	# test failure into two.
	sudo incus delete --force "$CONTAINER" 2>/dev/null || true
	echo "$CONTAINER is gone"
}

run() {
	# sudo, because the incus connection plugin shells out to `incus` and the
	# socket is root-only here. community.general lives in
	# /usr/lib/python3/dist-packages, which root sees.
	sudo -n ansible-playbook \
		-i "$ANSIBLE_DIR/inventory/incus-test.ini" \
		"$ANSIBLE_DIR/playbooks/invctl.yml" \
		"$@"
}

case "${1:-}" in
up) up ;;
down) down ;;
run)
	shift
	run "$@"
	;;
*)
	usage
	exit 1
	;;
esac
