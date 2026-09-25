FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS dirs
RUN mkdir -p /data

FROM scratch

COPY --from=dirs --chown=1000:1000 /data /app/data
COPY --chown=1000:1000 logcollector /app/logcollector

VOLUME /app/data

USER 1000:1000

# Export temp files are written next to the database, so /app/data must stay writable.
ENV HANNAH_LOGCOLLECTOR_DB_PATH=/app/data/logs.db

EXPOSE 50060

ENTRYPOINT ["/app/logcollector"]
