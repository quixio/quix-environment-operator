# Retain the released controller and runtime; only the trust bundle changes.
FROM quixanalytics/quix-environment-operator@sha256:bada63e9fd09fa79b58a2b36601a5598128b4fbdbe5ccc6b5a432115855a3608
COPY --chown=0:0 --chmod=0644 ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
