#!/bin/bash
set -e

# Usage:
#   First time:  curl -sSL https://raw.githubusercontent.com/jeffdafoe/llm-memory-plugin-salem-1692/main/install.sh -o /tmp/install.sh && sudo bash /tmp/install.sh
#   Re-install:  sudo bash /opt/llm-memory-salem-1692/install.sh
#   Deploy only: sudo bash /opt/llm-memory-salem-1692/deploy.sh

echo -e "\033[1;36m==================================="
echo "  ZBBS Installer"
echo -e "===================================\033[0m"
echo

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (sudo)"
    exit 1
fi

# Install dependencies
echo -e "\033[1m[1/4] Installing system dependencies...\033[0m"
apt update
apt install -y git ansible curl

# Clone repository
echo -e "\033[1m[2/4] Cloning ZBBS repository...\033[0m"
if [ -d "/opt/llm-memory-salem-1692/.git" ]; then
    echo "Git repo exists. Pulling latest..."
    cd /opt/llm-memory-salem-1692
    git pull
elif [ -d "/opt/llm-memory-salem-1692" ]; then
    echo "Directory exists (no git). Skipping clone."
else
    git clone https://github.com/jeffdafoe/llm-memory-plugin-salem-1692.git /opt/llm-memory-salem-1692
fi

# Run setup playbook (will prompt for secrets on first run)
echo -e "\033[1m[3/4] Running setup...\033[0m"
cd /opt/llm-memory-salem-1692/infrastructure
export ANSIBLE_CONFIG=/opt/llm-memory-salem-1692/infrastructure/ansible.cfg
ansible-playbook -i inventory/production.yml playbooks/setup.yml

# Run deploy playbook (run_migrations=true loads the schema baseline on a fresh
# DB, then applies any post-baseline migrations)
echo -e "\033[1m[4/4] Running deploy...\033[0m"
ansible-playbook -i inventory/production.yml playbooks/deploy.yml -e run_migrations=true

echo ""
echo -e "\033[1;32m==================================="
echo "  Installation complete!"
echo -e "===================================\033[0m"
echo ""
echo "To deploy updates later, run:"
echo "  sudo bash /opt/llm-memory-salem-1692/deploy.sh"
echo ""
