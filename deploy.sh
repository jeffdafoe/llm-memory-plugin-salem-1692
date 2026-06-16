#!/bin/bash
set -e

echo -e "\033[1;36m==================================="
echo "  Salem 1692 Deploy"
echo -e "===================================\033[0m"
echo

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (sudo)"
    exit 1
fi

# Pull latest
echo -e "\033[1m[1/3] Pulling latest code...\033[0m"
cd /opt/llm-memory-salem-1692
# fetch + hard reset rather than "git pull": a force-pushed / rewritten main (e.g. a
# history scrub) shares no common ancestor with the local clone, so a pull's merge dies
# with "refusing to merge unrelated histories" and wedges the deploy. Reset is immune.
# (Matches llm-memory-api's deploy.sh.) Untracked files like clients/ are left in place.
git fetch origin
git reset --hard origin/main

# Run deploy playbook
echo -e "\033[1m[2/3] Running deploy...\033[0m"
cd /opt/llm-memory-salem-1692/infrastructure
export ANSIBLE_CONFIG=/opt/llm-memory-salem-1692/infrastructure/ansible.cfg
ansible-playbook -i inventory/production.yml playbooks/deploy.yml -e run_migrations=true

# Verify service is running
echo -e "\033[1m[3/3] Verifying service...\033[0m"
systemctl is-active salem-engine

echo ""
echo -e "\033[1;32m==================================="
echo "  Deploy complete!"
echo -e "===================================\033[0m"
echo ""
