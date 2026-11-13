# anyview-next 顶层入口
#
#   make dev        日常开发：并行起 relay(air 热重载, :8787) + nuxt dev(默认 :3000，被占自动换)
#                   前端端口看启动日志，/api 已代理到 8787
#   make relay      单进程跑 relay(:8787)，托管 make web-build 产出的静态 SPA
#   make relay-mock 同上但走 -mock 假上游（离线调试用）
#   make web-build  生成 SPA 静态资源到 web/.output/public
#   make clean      清理构建产物
#
# 端口约定：8787 = relay；前端 dev 默认 3000，被占时 nuxt 自动换空闲端口

AIR_VERSION ?= v1.67.4
# token 落盘处；relay 热重载/重启复用它，避免重登踢掉网页端会话
# 不放 /tmp：tmp-clean.timer 两天清一次，token 蒸发=被迫重登顶掉网页端会话
STATE ?= $(HOME)/anyview-next-state.json

.PHONY: dev dev-relay dev-web relay relay-build relay-mock web-build test clean

dev:
	$(MAKE) -j2 dev-relay dev-web

dev-relay:
	@command -v air >/dev/null 2>&1 || go install github.com/air-verse/air@$(AIR_VERSION)
	@AIR_BIN=$$(command -v air || echo "$(HOME)/go/bin/air"); \
	cd relay && "$$AIR_BIN"

dev-web:
	cd web && bun run dev

relay-build:
	cd relay && go build -o relay .

web-build:
	cd web && bun run generate

relay: relay-build
	cd relay && ./relay -listen 127.0.0.1:8787 -state $(STATE) -web ../web/.output/public

relay-mock: relay-build
	cd relay && ./relay -listen 127.0.0.1:8787 -mock

test:
	cd relay && go test -race ./...
	cd web && bun run test

clean:
	rm -rf relay/tmp relay/relay relay/build-errors.log web/.nuxt web/.output
