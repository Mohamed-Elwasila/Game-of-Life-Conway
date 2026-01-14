.PHONY: help build clean test start-services stop-services start-broker start-workers run logs status

# Configuration
BROKER_PORT ?= 8030
BROKER_BACKUP_PORTS ?= 8040 8041
WORKER_PORTS ?= 8031 8032 8033 8034
LOG_DIR ?= ./test-logs
PID_DIR ?= ./pids

# Build output directories
BUILD_DIR ?= ./build

# Colors
YELLOW := \033[1;33m
GREEN := \033[0;32m
RED := \033[0;31m
NC := \033[0m

help:
	@echo "$(YELLOW)Distributed Game of Life - Makefile Commands$(NC)"
	@echo ""
	@echo "$(GREEN)Build Commands:$(NC)"
	@echo "  make build              - Build broker and worker binaries"
	@echo "  make clean              - Remove build artifacts, logs, and PIDs"
	@echo ""
	@echo "$(GREEN)Service Management:$(NC)"
	@echo "  make start-services     - Start broker and all workers"
	@echo "  make stop-services      - Stop all running services"
	@echo "  make restart-services   - Restart all services"
	@echo "  make status             - Check status of all services"
	@echo ""
	@echo "$(GREEN)Individual Service Control:$(NC)"
	@echo "  make start-broker       - Start broker only"
	@echo "  make start-workers      - Start workers only"
	@echo "  make stop-broker        - Stop broker only"
	@echo "  make stop-workers       - Stop workers only"
	@echo ""
	@echo "$(GREEN)Testing:$(NC)"
	@echo "  make test               - Run all tests (auto-starts services)"
	@echo "  make test-quick         - Run tests without race detector"
	@echo "  make coverage           - Generate coverage report"
	@echo ""
	@echo "$(GREEN)Running:$(NC)"
	@echo "  make run                - Start services and run the controller"
	@echo ""
	@echo "$(GREEN)Logs & Debugging:$(NC)"
	@echo "  make logs               - Show recent logs from all services"
	@echo "  make logs-broker        - Show broker logs"
	@echo "  make logs-workers       - Show worker logs"
	@echo "  make logs-follow        - Follow logs in real-time"

build: $(BUILD_DIR)/broker $(BUILD_DIR)/worker
	@echo "$(GREEN)Build complete$(NC)"

$(BUILD_DIR)/broker: broker/*.go distributed/*.go
	@mkdir -p $(BUILD_DIR)
	@echo "$(YELLOW)Building broker...$(NC)"
	@go build -o $(BUILD_DIR)/broker ./broker

$(BUILD_DIR)/worker: worker/*.go distributed/*.go gol/*.go util/*.go
	@mkdir -p $(BUILD_DIR)
	@echo "$(YELLOW)Building worker...$(NC)"
	@go build -o $(BUILD_DIR)/worker ./worker

clean:
	@echo "$(YELLOW)Cleaning up...$(NC)"
	@rm -rf $(BUILD_DIR) $(LOG_DIR) $(PID_DIR) coverage.out out/*.pgm
	@echo "$(GREEN)Clean complete$(NC)"

start-services: start-broker start-workers
	@echo "$(GREEN)All services started$(NC)"
	@sleep 1
	@$(MAKE) status

start-broker:
	@mkdir -p $(LOG_DIR) $(PID_DIR)
	@if [ -f $(PID_DIR)/broker.pid ] && kill -0 $$(cat $(PID_DIR)/broker.pid) 2>/dev/null; then \
		echo "$(YELLOW)Broker already running (PID: $$(cat $(PID_DIR)/broker.pid))$(NC)"; \
	else \
		echo "$(YELLOW)Starting broker on port $(BROKER_PORT)...$(NC)"; \
		go run ./broker -port $(BROKER_PORT) -primary=true -id=broker1 > $(LOG_DIR)/broker.log 2>&1 & \
		echo $$! > $(PID_DIR)/broker.pid; \
		sleep 1; \
		if kill -0 $$(cat $(PID_DIR)/broker.pid) 2>/dev/null; then \
			echo "$(GREEN)Broker started (PID: $$(cat $(PID_DIR)/broker.pid))$(NC)"; \
		else \
			echo "$(RED)Failed to start broker$(NC)"; \
			cat $(LOG_DIR)/broker.log; \
			rm -f $(PID_DIR)/broker.pid; \
			exit 1; \
		fi \
	fi

start-workers:
	@mkdir -p $(LOG_DIR) $(PID_DIR)
	@for port in $(WORKER_PORTS); do \
		if [ -f $(PID_DIR)/worker-$$port.pid ] && kill -0 $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null; then \
			echo "$(YELLOW)Worker on port $$port already running (PID: $$(cat $(PID_DIR)/worker-$$port.pid))$(NC)"; \
		else \
			echo "$(YELLOW)Starting worker on port $$port...$(NC)"; \
			go run ./worker -port $$port > $(LOG_DIR)/worker-$$port.log 2>&1 & \
			echo $$! > $(PID_DIR)/worker-$$port.pid; \
			sleep 0.5; \
			if kill -0 $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null; then \
				echo "$(GREEN)Worker started on port $$port (PID: $$(cat $(PID_DIR)/worker-$$port.pid))$(NC)"; \
			else \
				echo "$(RED)Failed to start worker on port $$port$(NC)"; \
				cat $(LOG_DIR)/worker-$$port.log; \
				rm -f $(PID_DIR)/worker-$$port.pid; \
				exit 1; \
			fi \
		fi \
	done

stop-services: stop-workers stop-broker
	@echo "$(GREEN)All services stopped$(NC)"

stop-broker:
	@if [ -f $(PID_DIR)/broker.pid ]; then \
		if kill -0 $$(cat $(PID_DIR)/broker.pid) 2>/dev/null; then \
			echo "$(YELLOW)Stopping broker (PID: $$(cat $(PID_DIR)/broker.pid))...$(NC)"; \
			kill $$(cat $(PID_DIR)/broker.pid) 2>/dev/null || true; \
			sleep 1; \
			if kill -0 $$(cat $(PID_DIR)/broker.pid) 2>/dev/null; then \
				kill -9 $$(cat $(PID_DIR)/broker.pid) 2>/dev/null || true; \
			fi; \
			echo "$(GREEN)Broker stopped$(NC)"; \
		fi; \
		rm -f $(PID_DIR)/broker.pid; \
	else \
		echo "$(YELLOW)Broker not running$(NC)"; \
	fi

stop-workers:
	@for port in $(WORKER_PORTS); do \
		if [ -f $(PID_DIR)/worker-$$port.pid ]; then \
			if kill -0 $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null; then \
				echo "$(YELLOW)Stopping worker on port $$port (PID: $$(cat $(PID_DIR)/worker-$$port.pid))...$(NC)"; \
				kill $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null || true; \
				sleep 0.5; \
				if kill -0 $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null; then \
					kill -9 $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null || true; \
				fi; \
				echo "$(GREEN)Worker on port $$port stopped$(NC)"; \
			fi; \
			rm -f $(PID_DIR)/worker-$$port.pid; \
		fi \
	done

restart-services: stop-services
	@sleep 1
	@$(MAKE) start-services

status:
	@echo "$(YELLOW)Service Status:$(NC)"
	@echo ""
	@if [ -f $(PID_DIR)/broker.pid ] && kill -0 $$(cat $(PID_DIR)/broker.pid) 2>/dev/null; then \
		echo "$(GREEN)Broker:$(NC) Running (PID: $$(cat $(PID_DIR)/broker.pid), Port: $(BROKER_PORT))"; \
	else \
		echo "$(RED)Broker:$(NC) Not running"; \
	fi
	@echo ""
	@for port in $(WORKER_PORTS); do \
		if [ -f $(PID_DIR)/worker-$$port.pid ] && kill -0 $$(cat $(PID_DIR)/worker-$$port.pid) 2>/dev/null; then \
			echo "$(GREEN)Worker $$port:$(NC) Running (PID: $$(cat $(PID_DIR)/worker-$$port.pid))"; \
		else \
			echo "$(RED)Worker $$port:$(NC) Not running"; \
		fi \
	done

test:
	@echo "$(YELLOW)Running tests with distributed infrastructure...$(NC)"
	@./run-tests.sh

test-quick: start-services
	@echo "$(YELLOW)Running quick tests...$(NC)"
	@sleep 2
	@go test -v -timeout 5m ./... || ($(MAKE) stop-services && exit 1)
	@$(MAKE) stop-services

coverage: start-services
	@echo "$(YELLOW)Generating coverage report...$(NC)"
	@sleep 2
	@go test -v -race -coverprofile=coverage.out -timeout 5m ./... || ($(MAKE) stop-services && exit 1)
	@go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -n 1
	@echo "$(GREEN)Coverage report generated: coverage.html$(NC)"
	@$(MAKE) stop-services

run: start-services
	@echo "$(YELLOW)Starting controller...$(NC)"
	@sleep 2
	@go run . || true
	@$(MAKE) stop-services

logs:
	@echo "$(YELLOW)=== Broker Logs ===$(NC)"
	@tail -n 20 $(LOG_DIR)/broker.log 2>/dev/null || echo "No broker logs found"
	@echo ""
	@for port in $(WORKER_PORTS); do \
		echo "$(YELLOW)=== Worker $$port Logs ===$(NC)"; \
		tail -n 10 $(LOG_DIR)/worker-$$port.log 2>/dev/null || echo "No logs found for worker $$port"; \
		echo ""; \
	done

logs-broker:
	@cat $(LOG_DIR)/broker.log 2>/dev/null || echo "No broker logs found"

logs-workers:
	@for port in $(WORKER_PORTS); do \
		echo "$(YELLOW)=== Worker $$port ===$(NC)"; \
		cat $(LOG_DIR)/worker-$$port.log 2>/dev/null || echo "No logs found"; \
		echo ""; \
	done

logs-follow:
	@echo "$(YELLOW)Following logs (Ctrl+C to stop)...$(NC)"
	@tail -f $(LOG_DIR)/*.log 2>/dev/null || echo "No logs found"
