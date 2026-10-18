# Postman: auth-service

| Файл | Назначение |
|---|---|
| `auth.postman_collection.json` | коллекция: служебные, сквозной сценарий, негативные, ручной прогон |
| `local.postman_environment.json` | `baseUrl = http://localhost:8081` |
| `arch-homework.postman_environment.json` | `baseUrl = http://arch.homework` (сдача ДЗ) |

Запуск: `make newman SERVICE=auth` (или `BASE_URL=http://arch.homework`). Прогон повторяем: телефон генерируется случайно.
Авторизация в сервисе — по **телефону** (E.164), не по email. Покрывает ДЗ-3/4/5/6 (Postman-часть) и проект (контрактные проверки auth).
