IMAGE ?= ghcr.io/rory-z/alertlens:latest
IMAGE_PLATFORMS ?=

KUBECONFIG ?= $(HOME)/.kube/flowmq-dev-tiger.yaml

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

export IMAGE KUBECONFIG
export E2E_NAMESPACE E2E_RELEASE E2E_SLACK_SECRET E2E_SLACK_CHANNEL
export E2E_ALERTMANAGER_NAMESPACE E2E_ALERTMANAGER_SERVICE
export E2E_ALERTMANAGER_URL E2E_ALERTMANAGER_PORT E2E_ALERTMANAGER_LOCAL_PORT
export E2E_HOLMES_NAMESPACE E2E_HOLMES_URL E2E_HOLMES_PORT

.PHONY: build push build-push slack-manifest e2e-deploy e2e-test e2e-undeploy

build:
	docker build --tag "$(IMAGE)" .

push:
	docker push "$(IMAGE)"

build-push:
ifneq ($(strip $(IMAGE_PLATFORMS)),)
	docker buildx build --platform "$(IMAGE_PLATFORMS)" --tag "$(IMAGE)" --push .
else
	$(MAKE) build push IMAGE="$(IMAGE)"
endif

slack-manifest:
	@./scripts/render-slack-manifest "$(SLACK_ENV)"

e2e-deploy:
	@./scripts/e2e-deploy deploy

e2e-test:
	@./scripts/e2e-test

e2e-undeploy:
	@./scripts/e2e-deploy undeploy
