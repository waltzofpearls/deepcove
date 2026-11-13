-- Large left the session VM sizes. A session saved with it keeps the CPU and memory Large gave it.
UPDATE sessions SET resources = '{"cpu":2,"memory_mb":4096}' WHERE resources = '{"size":"large"}';
