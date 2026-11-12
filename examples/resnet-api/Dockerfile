# syntax=docker/dockerfile:1
# Copy the published worker into a normal CUDA application image.
FROM saladtechnologies/salad-cloud-preflight@sha256:644fae3d6f287ee1dd0d890873e5684c951d953da1a60551d34b42071443beb9 AS preflight
# pytorch/pytorch:2.10.0-cuda12.8-cudnn9-runtime
FROM pytorch/pytorch@sha256:b85566342b86d13a67712e9315d40cdc2dad7f8d86df1aff3831f80835edbcca
ENV PYTHONUNBUFFERED=1 NVIDIA_VISIBLE_DEVICES=all NVIDIA_DRIVER_CAPABILITIES=compute,utility
# Reuse the base's pip and CUDA packages without modifying system Python.
RUN python -m venv --without-pip --system-site-packages /opt/workload-venv
ENV PATH="/opt/workload-venv/bin:${PATH}"
WORKDIR /opt/workload
COPY examples/resnet-api/requirements.txt ./
RUN python -m pip install --no-cache-dir -r requirements.txt
COPY examples/resnet-api/prepare_model.py ./
# Weights and labels are baked in; node startup does not download a model.
RUN TORCH_HOME=/tmp/model-cache python prepare_model.py && rm -rf /tmp/model-cache
COPY examples/resnet-api/app.py ./
COPY --from=preflight /salad-preflight /usr/local/bin/salad-preflight
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/salad-preflight", "--"]
CMD ["python", "/opt/workload/app.py"]
