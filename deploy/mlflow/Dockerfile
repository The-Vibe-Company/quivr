FROM python:3.12-slim
RUN pip install --no-cache-dir 'mlflow[auth]==3.16.1' 'psycopg[binary]>=3,<4'
ENV MLFLOW_DISABLE_TELEMETRY=true DO_NOT_TRACK=true MLFLOW_DISABLE_AGENT_HINT=1
COPY deploy/mlflow/entrypoint.py /opt/mlflow/entrypoint.py
CMD ["python", "/opt/mlflow/entrypoint.py"]
