# Протокол Horreum — спецификация 📡

[English](../protocol.md) | [Русский](protocol.md) | [中文](../zh/protocol.md)

---

## 1. Раскладка фрейма

Каждый запрос и ответ Horreum — это один фрейм (10-байтный LE заголовок).

```
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Magic (0x4848) | OpCode (1B) | Flags (1B)  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| KeyLength (2 LE) | ValueLength (4 LE)       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| ... Key ...   | ... Value ...              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Поле | Размер | Описание |
|------|--------|----------|
| Magic | 2 | `0x4848` (`"HH"`). Отвергает чужой трафик |
| OpCode | 1 | Код операции (см. §3) |
| Flags | 1 | В ответах: 0=OK, 1=ERROR |
| KeyLength | 2 LE | Длина ключа в байтах, макс. 65535 |
| ValueLength | 4 LE | Длина значения, ограничено 64 МиБ |
| Key | N | байты ключа |
| Value | M | байты значения |

Максимальный размер фрейма: **64 МиБ**.

### 1.1 Порядок байт

Little-endian. Magic `0x48 0x48`.

### 1.2 Проверка Magic

Неверный magic → `ErrBadMagic`.

---

## 2. Каталог опкодов

| Код | Имя | Req → Key | Req Value | Resp Value |
|------|------|-----------|-----------|------------|
| 0x01 | GET | да | empty | payload / empty |
| 0x02 | SET | да | payload | empty |
| 0x03 | DEL | да | empty | empty |
| 0x04 | SETEX | да | `[ttl:4][payload]` | empty |
| 0x05 | CAS | да | `[expLen:4][expected][new]` | empty / currentValue при mismatch |
| 0x06 | INCR | да | `[delta:8]` | `[newValue:8]` |
| 0x07 | SCAN | да (prefix) | `[cursor:8][count:4]` | `[nextCursor:8][n:4][...]` |
| 0x08 | DELPREFIX | да (prefix) | empty | `[deletedCount:8]` |
| 0x09 | HSET | да | `[fieldLen:2][field][value]` | empty |
| 0x0A | HGET | да | `[field]` | payload / empty |
| 0x0B | HDEL | да | `[field]` | empty |
| 0x0C | HGETALL | да | empty | `[n:4][...]` |
| 0x0D | LPUSH | да | `[elem]` | `[newLength:4]` |
| 0x0E | LPOP | да | empty | `[elem]` |
| 0x0F | RPUSH | да | `[elem]` | `[newLength:4]` |
| 0x10 | RPOP | да | empty | `[elem]` |
| 0x11 | LLEN | да | empty | `[length:4]` |
| 0x12 | SADD | да | `[member]` | empty |
| 0x13 | SREM | да | `[member]` | empty |
| 0x14 | SISMEMBER | да | `[member]` | empty |
| 0x15 | SMEMBERS | да | empty | `[n:4][...]` |

`0x16..0xFF` зарезервированы.

---

## 3. Семантика опкодов

### GET (0x01)
- Req: пустой value.
- Resp: OK с payload или ERROR (отсутствует/просрочен).

### SET (0x02)
- Req: key + value.
- Resp: OK или ERROR.

### DEL (0x03)
- Идемпотентно: всегда OK.

### SETEX (0x04)
- Req: key + `[ttl seconds][value]`.
- `expiresAt = now + ttl`. TTL=0 через SETEX невалиден.

### CAS (0x05)
- Req: key + `[expLen][expected][new]`.
- Read-then-write. На mismatch response несёт current value.

### INCR (0x06)
- Req: key + `[delta:u64]`. delta знаковое через бит 63.
- Текущее значение парсится как LE int64.

### SCAN (0x07)
- Req: prefix + `[cursor][count]`.
- Resp: `[nextCursor][n][(keyLen:2)(key)]×n`. cursor=0 — начало.
- Multi-shard: upper 16 бит = shard, lower 48 = inner.

### DELPREFIX (0x08)
- Атомарное удаление всех ключей с префиксом, по всем шардам.

### HSET/HGET/HDEL/HGETALL
Раскладка hash: `[count:4][(fieldLen:2)(field)(valueLen:4)(value)]×count`.
HGETALL возвращает все пары в порядке.

### LPUSH/LPOP/RPUSH/RPOP/LLEN
Раскладка list: `[count:4][(elemLen:4)(elem)]×count`. Пустой список → ключ удаляется.

### SADD/SREM/SISMEMBER/SMEMBERS
Раскладка set: `[count:4][(memberLen:2)(member)]×count`.

---

## 4. Статус ответа

| flags | Значение |
|-------|----------|
| 0x00 | OK |
| 0x01 | ERROR |

Flags в запросе должны быть нулём.

---

## 5. Потоковое поведение

### 5.1 Конвейеризация

Клиент может отправлять несколько запросов без ожидания ответа; сервер сохраняет порядок.

```
Клиент → Сервер: GET k1    SET k2 v2    GET k3    DEL k4
Сервер → Клиент: OK k1 v1  OK k2       OK k3 v3  OK k4
```

### 5.2 Частичные фреймы

Парсер буферизует неполные байты для следующего чтения.

### 5.3 Противодавление

Шардовая очередь воркера = 4096 job'ов. На переполнении сервер приостанавливает чтение.

---

## 6. Заметки реализации

### 6.1 Атомарность

Одна операция атомарна. Составные (HGETALL, SMEMBERS) атомарны с точки зрения API (single shard worker). Мульти-ключевые паттерны — нет.

### 6.2 Привязка соединения

По умолчанию по `(localAddr, remoteAddr)` → шардовый воркер.

### 6.3 Шифрование/сжатие

Метаданные сжатия/шифрования не передаются в wire-формате. Клиент всегда видит plain bytes.

### 6.4 Время

TTL базируется на часах хоста. Используйте NTP для синхронизации.

---

## 7. Примеры

### SET "user:42" → "Alice"

```
48 48 02 00 08 00 05 00 00 00   <- заголовок
75 73 65 72 3a 34 32            <- "user:42"
41 6c 69 63 65                  <- "Alice"
```

### GET "user:42"

Req:
```
48 48 01 00 08 00 00 00 00 00
75 73 65 72 3a 34 32
```

Resp OK:
```
48 48 01 00 08 00 05 00 00 00
75 73 65 72 3a 34 32
41 6c 69 63 65
```

Resp ERROR:
```
48 48 01 01 08 00 00 00 00 00
75 73 65 72 3a 34 32
```

### SETEX "session:abc" 60s TTL → "token"

```
48 48 04 00
0b 00
09 00 00 00            <- valLen=9
73 65 73 73 69 6f 6e 3a 61 62 63
3c 00 00 00            <- ttl=60
74 6f 6b 65 6e
```

### CAS "user:42" ожидание "Bob" → "Carol"

```
48 48 05 00
08 00
12 00 00 00            <- valLen=12
75 73 65 72 3a 34 32
03 00 00 00            <- expLen=3
42 6f 62               <- "Bob"
43 61 72 6f 6c        <- "Carol"
```

### SCAN prefix "user:" cursor=0 count=10

Req:
```
48 48 07 00
06 00
0c 00 00 00
75 73 65 72 3a
00 00 00 00 00 00 00 00   <- cursor=0
0a 00 00 00              <- count=10
```

Resp:
```
48 48 07 00
06 00
1f 00 00 00
75 73 65 72 3a
00 00 00 00 00 00 00 00   <- nextCursor=0 (done)
02 00 00 00              <- n=2
08 00 75 73 65 72 3a 34 32
08 00 75 73 65 72 3a 34 33
```

---

## 8. Версионирование

Протокол стабилен между минорными версиями. Новые опкоды в конце таблицы. Неизвестный опкод → `ErrUnknownOp`.

## 9. Референтные клиенты

`examples/client.{go,py,js}` демонстрируют все 21 опкод.
