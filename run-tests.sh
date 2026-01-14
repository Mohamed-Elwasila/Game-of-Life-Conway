#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
BROKER_PORT=8030
WORKER_PORTS=(8031 8032 8033 8034)
LOG_DIR="./test-logs"
STARTUP_WAIT=3
SHUTDOWN_WAIT=2

# PID tracking
BROKER_PID=""
WORKER_PIDS=()

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up processes...${NC}"

    # Kill workers
    for pid in "${WORKER_PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
    done

    # Kill broker
    if [ -n "$BROKER_PID" ] && kill -0 "$BROKER_PID" 2>/dev/null; then
        kill "$BROKER_PID" 2>/dev/null || true
    fi

    # Wait for graceful shutdown
    sleep "$SHUTDOWN_WAIT"

    # Force kill if still running
    for pid in "${WORKER_PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null || true
        fi
    done

    if [ -n "$BROKER_PID" ] && kill -0 "$BROKER_PID" 2>/dev/null; then
        kill -9 "$BROKER_PID" 2>/dev/null || true
    fi

    echo -e "${GREEN}Cleanup complete${NC}"
}

# Set trap to cleanup on exit
trap cleanup EXIT INT TERM

# Create log directory
mkdir -p "$LOG_DIR"

echo -e "${YELLOW}Starting distributed Game of Life test infrastructure...${NC}\n"

# Start broker
echo -e "${YELLOW}[1/3] Starting broker on port $BROKER_PORT...${NC}"
go run ./broker -port "$BROKER_PORT" -primary=true -id=broker1 > "$LOG_DIR/broker.log" 2>&1 &
BROKER_PID=$!

if ! kill -0 "$BROKER_PID" 2>/dev/null; then
    echo -e "${RED}Failed to start broker${NC}"
    cat "$LOG_DIR/broker.log"
    exit 1
fi

echo -e "${GREEN}Broker started (PID: $BROKER_PID)${NC}"

# Start workers
echo -e "\n${YELLOW}[2/3] Starting workers...${NC}"
for port in "${WORKER_PORTS[@]}"; do
    go run ./worker -port "$port" > "$LOG_DIR/worker-$port.log" 2>&1 &
    pid=$!
    WORKER_PIDS+=("$pid")

    if ! kill -0 "$pid" 2>/dev/null; then
        echo -e "${RED}Failed to start worker on port $port${NC}"
        cat "$LOG_DIR/worker-$port.log"
        exit 1
    fi

    echo -e "${GREEN}Worker started on port $port (PID: $pid)${NC}"
done

# Wait for services to initialize
echo -e "\n${YELLOW}Waiting ${STARTUP_WAIT}s for services to initialize...${NC}"
sleep "$STARTUP_WAIT"

# Verify services are still running
echo -e "\n${YELLOW}Verifying services...${NC}"
all_running=true

if ! kill -0 "$BROKER_PID" 2>/dev/null; then
    echo -e "${RED}Broker died during startup${NC}"
    cat "$LOG_DIR/broker.log"
    all_running=false
fi

for i in "${!WORKER_PIDS[@]}"; do
    pid="${WORKER_PIDS[$i]}"
    port="${WORKER_PORTS[$i]}"
    if ! kill -0 "$pid" 2>/dev/null; then
        echo -e "${RED}Worker on port $port died during startup${NC}"
        cat "$LOG_DIR/worker-$port.log"
        all_running=false
    fi
done

if [ "$all_running" = false ]; then
    echo -e "\n${RED}Service startup failed. Check logs in $LOG_DIR${NC}"
    exit 1
fi

echo -e "${GREEN}All services running${NC}"

# Show recent logs
echo -e "\n${YELLOW}Recent broker log:${NC}"
tail -n 5 "$LOG_DIR/broker.log" || true

echo -e "\n${YELLOW}Recent worker logs:${NC}"
for port in "${WORKER_PORTS[@]}"; do
    echo -e "${YELLOW}Worker $port:${NC}"
    tail -n 3 "$LOG_DIR/worker-$port.log" || true
done

# Run tests
echo -e "\n${YELLOW}[3/3] Running tests...${NC}\n"
if go test -v -race -coverprofile=coverage.out -timeout 5m ./...; then
    echo -e "\n${GREEN}Tests passed!${NC}\n"

    # Show coverage
    echo -e "${YELLOW}Coverage summary:${NC}"
    go tool cover -func=coverage.out | tail -n 1

    exit_code=0
else
    echo -e "\n${RED}Tests failed!${NC}\n"

    # Show logs on failure
    echo -e "${YELLOW}Broker log:${NC}"
    cat "$LOG_DIR/broker.log"

    echo -e "\n${YELLOW}Worker logs:${NC}"
    for port in "${WORKER_PORTS[@]}"; do
        echo -e "\n${YELLOW}Worker $port:${NC}"
        cat "$LOG_DIR/worker-$port.log"
    done

    exit_code=1
fi

exit $exit_code
