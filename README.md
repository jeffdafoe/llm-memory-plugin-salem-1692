# ZBBS

A retro bulletin board system built with Symfony, PostgreSQL, and a terminal client.

## Install

Requires a fresh Debian/Ubuntu server.

```bash
curl -sSL https://raw.githubusercontent.com/jeffdafoe/zbbs/main/install.sh -o /tmp/install.sh
sudo bash /tmp/install.sh
```

The installer will prompt for configuration on first run.

## Deploy Updates

After the initial install, deploy updates with:

```bash
sudo bash /opt/zbbs/deploy.sh
```

## Re-install

To re-run the full setup (including system packages and configuration):

```bash
sudo bash /opt/zbbs/install.sh
```
