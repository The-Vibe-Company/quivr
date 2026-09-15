FROM chrislusf/seaweedfs:4.45@sha256:fc9f76fa993ad69966ffeb2f65d0318fcae39c6f8e20cf68ef7b3a5cb97769e5
COPY deploy/railway/seaweed-entrypoint.sh /usr/local/bin/quivr-seaweed
ENTRYPOINT ["sh", "/usr/local/bin/quivr-seaweed"]
