#!/usr/bin/env bash
set -euo pipefail

echo "  ┌──────────────────────────────────────────────┐"
echo "  │  devtop — docs + tickets with AI             │"
echo "  │                                              │"
echo "  │  Scaffolds .devtop/ in the current directory │"
echo "  └──────────────────────────────────────────────┘"
echo ""

mkdir -p .devtop/docs .devtop/tickets .devtop/threads .devtop/data

# Sample index.mdx
if [ ! -f .devtop/docs/index.mdx ]; then
  cat > .devtop/docs/index.mdx << 'MDXEOF'
---
title: "Project Overview"
---

# Welcome to devtop

This is your project's documentation and ticket system.

## Quick Start

Open the chat panel and ask the agent to:

- Create a new document
- Open a ticket
- Add a diagram
- Update the project overview
MDXEOF
  echo "✔ Created .devtop/docs/index.mdx"
fi

# .gitignore
if [ ! -f .devtop/.gitignore ]; then
  cat > .devtop/.gitignore << 'GIEOF'
threads/
data/
GIEOF
  echo "✔ Created .devtop/.gitignore"
fi

# Determine mode
HAS_JUST=false
command -v just &>/dev/null && HAS_JUST=true
HAS_DOCKER=false
command -v docker &>/dev/null && HAS_DOCKER=true

echo ""
echo "  ┌──────────────────────────────────────────────┐"
echo "  │  .devtop/ is ready                           │"
echo "  │                                              │"

if [ "$HAS_JUST" = true ] && [ -f justfile ]; then
  echo "  │  Run: just devtop            (local)        │"
  if [ "$HAS_DOCKER" = true ]; then
    echo "  │  Run: just devtop docker     (container)    │"
  fi
elif [ "$HAS_DOCKER" = true ]; then
  echo "  │  Run:                                        │"
  echo "  │    docker run ... ghcr.io/synlace/devtop     │"
  echo "  │                                              │"
  echo "  │  Or install locally:                         │"
  echo "  │    pip install fastapi uvicorn jinja2 ...    │"
  echo "  │    python /path/to/devtop/app.py             │"
fi

echo "  │                                              │"
echo "  │  Set AI_API_KEY env var or use .env          │"
echo "  └──────────────────────────────────────────────┘"