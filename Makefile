IMAGE ?= ghcr.io/rory-z/alertlens:latest
IMAGE_PLATFORMS ?=

KUBECONFIG ?= $(HOME)/.kube/flowmq-dev-tiger.yaml
export KUBECONFIG

E2E_NAMESPACE ?= alertlens-e2e
E2E_RELEASE ?= alertlens-e2e
E2E_SLACK_SECRET ?= alertlens-e2e-slack
E2E_SLACK_CHANNEL ?= C099FMSGNEQ
E2E_ALERTMANAGER_NAMESPACE ?= victoria
E2E_ALERTMANAGER_SERVICE ?= vmalertmanager-victoria-metrics-k8s-stack
E2E_ALERTMANAGER_URL ?= http://vmalertmanager-victoria-metrics-k8s-stack.victoria.svc:9093
E2E_ALERTMANAGER_PORT ?= 9093
E2E_ALERTMANAGER_LOCAL_PORT ?= 19093
E2E_HOLMES_NAMESPACE ?= holmes
E2E_HOLMES_URL ?= http://holmes-holmes.holmes.svc:80
E2E_HOLMES_PORT ?= 80

.PHONY: build push build-push e2e-deploy e2e-test e2e-undeploy

build:
	docker build --tag "$(IMAGE)" .

push:
	docker push "$(IMAGE)"

build-push:
	@if [ -n "$(strip $(IMAGE_PLATFORMS))" ]; then \
		docker buildx build --platform "$(IMAGE_PLATFORMS)" --tag "$(IMAGE)" --push .; \
	else \
		$(MAKE) build push IMAGE="$(IMAGE)"; \
	fi

e2e-deploy:
	@set -eu; \
	image='$(IMAGE)'; \
	case "$$image" in *@*) echo "IMAGE must use repository:tag, not a digest" >&2; exit 1;; esac; \
	case "$${image##*/}" in *:*) ;; *) echo "IMAGE must include a tag" >&2; exit 1;; esac; \
	repository=$${image%:*}; tag=$${image##*:}; \
	if [ -z "$$tag" ]; then echo "IMAGE must include a tag" >&2; exit 1; fi; \
	kubectl create namespace "$(E2E_NAMESPACE)" --dry-run=client -o yaml | kubectl apply -f -; \
	if ! kubectl -n "$(E2E_NAMESPACE)" get secret "$(E2E_SLACK_SECRET)" >/dev/null 2>&1; then \
		echo "missing Secret $(E2E_NAMESPACE)/$(E2E_SLACK_SECRET); create bot-token and app-token keys first" >&2; \
		exit 1; \
	fi; \
	for key in bot-token app-token; do \
		value=$$(kubectl -n "$(E2E_NAMESPACE)" get secret "$(E2E_SLACK_SECRET)" -o "jsonpath={.data.$$key}"); \
		if [ -z "$$value" ]; then echo "Secret $(E2E_SLACK_SECRET) is missing $$key" >&2; exit 1; fi; \
	done; \
	helm upgrade --install "$(E2E_RELEASE)" charts/alertlens \
		--namespace "$(E2E_NAMESPACE)" \
		--wait --timeout 5m \
		--set-string image.repository="$$repository" \
		--set-string image.tag="$$tag" \
		--set image.pullPolicy=Always \
		--set-string slack.existingSecret="$(E2E_SLACK_SECRET)" \
		--set-string 'slack.alertChannels[0]=$(E2E_SLACK_CHANNEL)' \
		--set-string alertmanagerURL="$(E2E_ALERTMANAGER_URL)" \
		--set-string holmesURL="$(E2E_HOLMES_URL)" \
		--set-string holmesResponseLanguage=zh-CN \
		--set-string 'networkPolicy.internalEgress[0].namespace=$(E2E_ALERTMANAGER_NAMESPACE)' \
		--set 'networkPolicy.internalEgress[0].ports[0]=$(E2E_ALERTMANAGER_PORT)' \
		--set-string 'networkPolicy.internalEgress[1].namespace=$(E2E_HOLMES_NAMESPACE)' \
		--set 'networkPolicy.internalEgress[1].ports[0]=$(E2E_HOLMES_PORT)'

e2e-test:
	@set -eu; \
	helm status "$(E2E_RELEASE)" -n "$(E2E_NAMESPACE)" >/dev/null 2>&1 || { echo "run make e2e-deploy first" >&2; exit 1; }; \
	deployment=$$(kubectl -n "$(E2E_NAMESPACE)" get deployment -l "app.kubernetes.io/instance=$(E2E_RELEASE)" -o jsonpath='{.items[0].metadata.name}'); \
	if [ -z "$$deployment" ]; then echo "AlertLens deployment not found" >&2; exit 1; fi; \
	kubectl -n "$(E2E_NAMESPACE)" wait --for=condition=Available "deployment/$$deployment" --timeout=30s >/dev/null; \
	token=$$(kubectl -n "$(E2E_NAMESPACE)" get secret "$(E2E_SLACK_SECRET)" -o jsonpath='{.data.bot-token}' | base64 -d); \
	if [ -z "$$token" ]; then echo "Secret $(E2E_SLACK_SECRET) is missing bot-token" >&2; exit 1; fi; \
	log=$$(mktemp); \
	kubectl -n "$(E2E_ALERTMANAGER_NAMESPACE)" port-forward "service/$(E2E_ALERTMANAGER_SERVICE)" "$(E2E_ALERTMANAGER_LOCAL_PORT):$(E2E_ALERTMANAGER_PORT)" >"$$log" 2>&1 & \
	forward_pid=$$!; \
	cleanup() { kill "$$forward_pid" 2>/dev/null || true; wait "$$forward_pid" 2>/dev/null || true; rm -f "$$log"; }; \
	trap cleanup EXIT INT TERM; \
	attempt=0; \
	while ! grep -q 'Forwarding from' "$$log"; do \
		if ! kill -0 "$$forward_pid" 2>/dev/null; then cat "$$log" >&2; exit 1; fi; \
		attempt=$$((attempt + 1)); \
		if [ "$$attempt" -ge 30 ]; then cat "$$log" >&2; echo "timed out starting Alertmanager port-forward" >&2; exit 1; fi; \
		sleep 1; \
	done; \
	ALERTLENS_E2E=1 \
	ALERTMANAGER_URL="http://127.0.0.1:$(E2E_ALERTMANAGER_LOCAL_PORT)" \
	SLACK_BOT_TOKEN="$$token" \
	E2E_SLACK_CHANNEL="$(E2E_SLACK_CHANNEL)" \
	go test -v -timeout=60m -run '^TestE2E$$' .

e2e-undeploy:
	helm uninstall "$(E2E_RELEASE)" --namespace "$(E2E_NAMESPACE)" --ignore-not-found --wait --timeout 5m
