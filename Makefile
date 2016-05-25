.PHONY: all binary test plugin ipam ut clean update-version

# Version of calico/build to use.
BUILD_VERSION=latest

SRCFILES=$(shell find calico_cni -type f ! -path calico_cni/version.py) ipam.py
LOCAL_IP_ENV?=$(shell ip route get 8.8.8.8 | head -1 | cut -d' ' -f8)

K8S_VERSION=1.2.3

default: all
all: binary test
binary: update-version dist/calico dist/calico-ipam
test: ut
plugin: dist/calico
ipam: dist/calico-ipam

# Makes the IPAM plugin.
dist/calico-ipam: $(SRCFILES) 
	docker run --rm \
	-v `pwd`:/code \
	calico/build:$(BUILD_VERSION) \
	pyinstaller ipam.py -ayF -n calico-ipam

# Updates the version information in version.py
update-version:
	echo "# Auto-generated contents.  Do not manually edit" > calico_cni/version.py
	echo "# or check in this file." >> calico_cni/version.py
	echo "__version__ = '$(shell git describe --tags)'" >> calico_cni/version.py
	echo "__commit__ = '$(shell git rev-parse HEAD)'" >> calico_cni/version.py
	echo "__branch__ = '$(shell git rev-parse --abbrev-ref HEAD)'" >> calico_cni/version.py

# Copy the plugin into place
deploy-rkt: dist/calico
	cp dist/calico /etc/rkt/net.d

# Run the unit tests.
ut: update-version
	docker run --rm -v `pwd`:/code \
	calico/test \
	nosetests tests/unit -c nose.cfg

# Makes tests on Circle CI.
test-circle: update-version dist/calico dist/calico-ipam
	# Can't use --rm on circle
	# Circle also requires extra options for reporting.
	docker run \
	-v `pwd`:/code \
	-v $(CIRCLE_TEST_REPORTS):/circle_output \
	-e COVERALLS_REPO_TOKEN=$(COVERALLS_REPO_TOKEN) \
	calico/test sh -c \
	'nosetests tests -c nose.cfg \
	--with-xunit --xunit-file=/circle_output/output.xml; RC=$$?;\
	[[ ! -z "$$COVERALLS_REPO_TOKEN" ]] && coveralls || true; exit $$RC'

clean:
	-rm -f *.created
	find . -name '*.pyc' -exec rm -f {} +
	-sudo rm -rf dist
	-docker run -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/docker:/var/lib/docker --rm martin/docker-cleanup-volumes
	rm -f calico_cni/version.py

## Run etcd in a container. Generally useful.
run-etcd:
	@-docker rm -f calico-etcd
	docker run --detach \
	--net=host \
	--name calico-etcd quay.io/coreos/etcd:v2.2.2 \
	--advertise-client-urls "http://$(LOCAL_IP_ENV):2379,http://127.0.0.1:2379,http://$(LOCAL_IP_ENV):4001,http://127.0.0.1:4001" \
	--listen-client-urls "http://0.0.0.0:2379,http://0.0.0.0:4001"

run-kubernetes-master: stop-kubernetes-master run-etcd  kubectl # binary dist/calicoctl
	mkdir -p net.d
	echo '{"name": "calico-k8s-network","type": "calico","etcd_authority": "10.0.2.15:2379","log_level": "debug","policy": {"type": "k8s","k8s_api_root": "http://127.0.0.1:8080/api/v1/"},"ipam": {"type": "host-local", "subnet": "10.0.0.0/8"}}' >net.d/10-calico.conf
	# Run the kubelet which will launch the master components in a pod.
	docker run \
		--volume=/:/rootfs:ro \
		--volume=/sys:/sys:ro \
		--volume=/var/lib/docker/:/var/lib/docker:rw \
		--volume=/var/lib/kubelet/:/var/lib/kubelet:rw \
		--volume=`pwd`/dist:/opt/cni/bin \
		--volume=`pwd`/net.d:/etc/cni/net.d \
		--volume=/var/run:/var/run:rw \
		--net=host \
		--pid=host \
		--privileged=true \
		--name calico-kubelet-master \
		-d \
		gcr.io/google_containers/hyperkube-amd64:v${K8S_VERSION} \
		/hyperkube kubelet \
			--containerized \
			--hostname-override="127.0.0.1" \
			--address="0.0.0.0" \
			--api-servers=http://localhost:8080 \
			--config=/etc/kubernetes/manifests-multi \
			--cluster-dns=10.0.0.10 \
			--network-plugin=cni \
			--network-plugin-dir=/etc/cni/net.d \
			--cluster-domain=cluster.local \
			--allow-privileged=true --v=2

	# Start the calico node
	sudo dist/calicoctl node

stop-kubernetes-master:
	# Stop any existing kubelet that we started
	-docker rm -f calico-kubelet-master

	# Remove any pods that the old kubelet may have started.
	-docker rm -f $$(docker ps | grep k8s_ | awk '{print $$1}')

run-kube-proxy:
	-docker rm -f calico-kube-proxy
	docker run --name calico-kube-proxy -d --net=host --privileged gcr.io/google_containers/hyperkube:v$(K8S_VERSION) /hyperkube proxy --master=http://127.0.0.1:8080 --v=2

kubectl:
	wget http://storage.googleapis.com/kubernetes-release/release/v$(K8S_VERSION)/bin/linux/amd64/kubectl
	chmod 755 kubectl

dist/calicoctl:
	mkdir -p dist
	sudo chmod a+w dist
	curl -o dist/calicoctl -L https://github.com/projectcalico/calico-containers/releases/download/v0.17.0/calicoctl
	chmod +x dist/calicoctl

glide:
	go get github.com/Masterminds/glide
	ln -s $$GOPATH/bin/glide glide

vendor: glide
	./glide up -s -v

dist/calico: $(shell find vendor -type f) flannel_build.created calico.go
	mkdir -p dist
	docker run --rm \
	-v ${PWD}/dist:/mnt/artifacts \
	-v ${PWD}:/go/src/github.com/projectcalico/calico-cni:ro \
	flannel_build bash -c '\
		go build -o /mnt/artifacts/calico -ldflags "-extldflags -static \
		-X github.com/projectcalico/calico-cni/version.Version=$(shell git describe --dirty)" calico.go; \
		chown -R $(shell id -u):$(shell id -u) /mnt/artifacts'
#	go build -v -o dist/caligo calico.go

flannel_build.created: Dockerfile
	docker build -t flannel_build .
	touch flannel_build.created

