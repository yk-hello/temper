# 构建：打包出来的是静态文件，不分 x86、ARM，所以只在构建机器自己的架构上跑一次（$BUILDPLATFORM）。
# 不这样的话，release.yml 编 arm64 镜像时要在模拟环境里跑 npm ci 和打包，几个小时都跑不完
FROM --platform=$BUILDPLATFORM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# 运行：nginx 提供页面，并把 /api 和 /ws 转给后端
FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
