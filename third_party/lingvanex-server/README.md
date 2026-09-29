# Lingvanex translation server

Offline machine translation for bellingua: [CTranslate2](https://github.com/OpenNMT/CTranslate2)
+ SentencePiece over Lingvanex models, behind a tiny HTTP API:

    POST http://127.0.0.1:8000/?from=en&to=de     (body: text, one segment per line)
    -> 200 text/plain, the translation line by line

`bellingua serve` starts and stops this server by itself (see `lingvanex` in
`bellingua.example.yaml`); `start.bat` runs it standalone for debugging.

## Setup

```powershell
py -m pip install -r requirements.txt
```

Models live next to `server.py`, one folder per language pair, and are loaded
on the first request for that pair (they are not in git — copy them here):

    <src>_<tgt>/1/{model.bin, config.json, <src>.spm.model, <tgt>.spm.model, *_vocabulary.txt}
    e.g. en_de/1/{...}

## Tuning

`BEAM_SIZE`, `LENGTH_PENALTY`, `COMPUTE_TYPE` and `MAX_DECODING_LENGTH`
determine the output and are deliberately locked. Only the CPU knobs
`INTER_THREADS` (parallel requests; keep bellingua's `mt.concurrency` at or
below it) and `INTRA_THREADS` (cores per request) may be changed for speed.
