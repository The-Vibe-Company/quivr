FROM temporalio/temporal:1.8.3@sha256:cea463d98a8d6def4420f903ea5c3fcd0d85c8d10fbcc2770a50c12fff2eb26d
USER root
CMD ["server", "start-dev", "--ip", "0.0.0.0", "--headless", "--db-filename", "/data/temporal.db"]
