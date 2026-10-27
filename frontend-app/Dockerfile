FROM node:22.22-alpine AS build
RUN apk upgrade --no-cache
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --include=dev
COPY . .
RUN npm run build
FROM nginxinc/nginx-unprivileged:1.30-alpine AS runtime
USER root
RUN apk upgrade --no-cache
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/apps/doctor/dist /usr/share/nginx/html/doctor
COPY --from=build /app/apps/patient/dist /usr/share/nginx/html/patient
EXPOSE 8080
USER 101
