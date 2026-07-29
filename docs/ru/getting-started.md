# Быстрый старт с Horreum 🚀

[English](../getting-started.md) | [Русский](getting-started.md) | [中文](../zh/getting-started.md)

---

## 1. Установка

### Требования
- **Go**: Версия 1.22 или выше.
- **Операционная система**: Linux (рекомендуется для нативного `mmap`), macOS или Windows.

### Сборка из исходников

```bash
# Клонирование репозитория
git clone https://github.com/horreum/horreum.git
cd horreum

# Сборка бинарного файла
go build -o horreum ./cmd/horreum

# Проверка установки
./horreum --version
```

---

## 2. Запуск Horreum

### Запуск со стандартной конфигурацией

По умолчанию запуск Horreum поднимает кеш с 4 шардами, регионом 256 MiB на шард и слушающим TCP порт `:7373`:

```bash
./horreum --config config.yaml
```

### Запуск в режиме персистентности (сохранения на диск)

Для включения Write-Ahead Logging (WAL) и гарантированной записи на диск:

```bash
./horreum --config config.yaml --persistent-path=/var/lib/horreum --durable=true
```

### Запуск через Docker

```bash
# Сборка образа
docker build -t horreum:latest .

# Запуск контейнера в фоновом режиме
docker run -d \
  -p 7373:7373 \
  -p 9090:9090 \
  -v $(pwd)/config.yaml:/etc/horreum/config.yaml \
  --name horreum-server \
  horreum:latest
```

---

## 3. Подключение клиентов

Horreum общается по высокопроизводительному бинарному протоколу через TCP порт `7373`. Готовые клиенты доступны в папке [`examples/`](file:///e:/pet/horreum/examples/).
