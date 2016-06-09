# TODO sort out phoney
.PHONY: all binary test plugin ipam ut clean update-version

# TODO - use proper SRCFILES
SRCFILES=calico.go
# TODO - make the IP docker-machine compatible
#LOCAL_IP_ENV?=$(shell docker-machine ip)
LOCAL_IP_ENV?=$(ip route get 8.8.8.8 | head -1 | cut -d' ' -f8)

K8S_VERSION=1.2.4
CALICO_NODE_VERSION=0.19.0

# Ensure that the dist directory is always created
MAKE_SURE_DIST_EXIST := $(shell mkdir -p dist)

BUILD_TAGS?=

default: all
all: binary test
binary: update-version dist/calico dist/calico-ipam
test: ut
plugin: dist/calico
ipam: dist/calico-ipam

# Run the unit tests.
ut: dist/calico
	sudo ETCD_IP=127.0.0.1 HOSTNAME=mbp PLUGIN=calico GOPATH=/home/tom/go /home/tom/go/bin/ginkgo


# Run the unit tests, watching for changes.
ut-watch:
	sudo ETCD_IP=127.0.0.1 HOSTNAME=mbp PLUGIN=calico GOPATH=/home/tom/go /home/tom/go/bin/ginkgo watch

clean:
	-sudo rm -rf dist

## Run etcd in a container.
run-etcd:
	@-docker rm -f calico-etcd
	docker run --detach \
	--net=host \
	--name calico-etcd quay.io/coreos/etcd:v2.3.6 \
	--advertise-client-urls "http://127.0.0.1:2379,http://127.0.0.1:4001" \
	--listen-client-urls "http://0.0.0.0:2379,http://0.0.0.0:4001"

# TODO - sort out deps
run-kubernetes-master: stop-kubernetes-master run-etcd  # binary dist/calicoctl
	echo Get kubectl from http://storage.googleapis.com/kubernetes-release/release/v$(K8S_VERSION)/bin/linux/amd64/kubectl
	mkdir -p net.d
	#echo '{"name": "calico-k8s-network","type": "calico","etcd_authority": "10.0.2.15:2379","log_level": "debug","policy": {"type": "k8s","k8s_api_root": "http://127.0.0.1:8080/api/v1/"},"ipam": {"type": "host-local", "subnet": "10.0.0.0/8"}}' >net.d/10-calico.conf
	echo '{"name": "calico-k8s-network","type": "calico","etcd_authority": "10.0.2.15:2379","log_level": "debug","policy": {"type": "k8s","k8s_api_root": "http://127.0.0.1:8080"},"ipam": {"type": "host-local", "subnet": "10.0.0.0/8"}}' >net.d/10-calico.conf
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

dist/calicoctl:
	curl -o dist/calicoctl -L https://github.com/projectcalico/calico-containers/releases/download/v$(CALICO_NODE_VERSION)/calicoctl
	chmod +x dist/calicoctl

glide:
	go install github.com/Masterminds/glide

vendor:
	glide up -strip-vcs -strip-vendor --update-vendored --all-dependencies

dist/calico: calico.go
	go build -v --tags "$(BUILD_TAGS)" -o dist/calico -ldflags "-extldflags -static -X main.VERSION=$(shell git describe --tags --dirty)" calico.go;

dist/calico-ipam: calico-ipam.go
		go build -o /mnt/artifacts/calico-ipam -ldflags "-extldflags -static \
		-X github.com/projectcalico/calico-cni/version.Version=$(shell git describe --tags --dirty)" ipam/calico-ipam.go; \


go_test: dist/calico dist/host-local dist/calipo
	docker run -ti --rm --privileged \
	--hostname cnitests \
	-e ETCD_IP=$(LOCAL_IP_ENV) \
	-e PLUGIN=calico \
	-v ${PWD}:/go/src/github.com/projectcalico/calico-cni:ro \
	flannel_build bash -c '\
		go test -v github.com/projectcalico/calico-cni/tests'

python_test: dist/calipo dist/host-local
	docker run -ti --rm --privileged \
	--hostname cnitests \
	-e ETCD_IP=$(LOCAL_IP_ENV) \
	-e PLUGIN=calipo \
	-v ${PWD}:/go/src/github.com/projectcalico/calico-cni:ro \
	flannel_build bash -c '\
		go test -v github.com/projectcalico/calico-cni/tests'

dist/host-local:
	mkdir -p dist
	curl -L https://github.com/containernetworking/cni/releases/download/v0.2.2/cni-v0.2.2.tgz | tar -zxv -C dist

dist/calipo:
	mkdir -p dist
	curl -L -o dist/calipo https://github.com/projectcalico/calico-cni/releases/download/v1.3.1/calico
	chmod +x dist/calipo

# Copy the plugin into place
deploy-rkt: dist/calico
	cp dist/calico /etc/rkt/net.d