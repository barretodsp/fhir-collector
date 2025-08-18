#!/bin/bash
set -e

echo "Criando DLQ FIFO..."
aws --endpoint-url=http://localhost:4566 sqs create-queue \
    --queue-name fhir-ingestion-dlq.fifo \
    --attributes '{"FifoQueue":"true"}'

DLQ_URL=$(aws --endpoint-url=http://localhost:4566 sqs get-queue-url \
    --queue-name fhir-ingestion-dlq.fifo \
    --query 'QueueUrl' --output text)

DLQ_ARN=$(aws --endpoint-url=http://localhost:4566 sqs get-queue-attributes \
    --queue-url $DLQ_URL \
    --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

echo "Criando fila FIFO principal com DLQ associada..."
aws --endpoint-url=http://localhost:4566 sqs create-queue \
    --queue-name fhir-ingestion.fifo \
    --attributes '{
        "FifoQueue": "true",
        "RedrivePolicy": "{\"deadLetterTargetArn\":\"'"$DLQ_ARN"'\",\"maxReceiveCount\":\"3\"}",
        "ContentBasedDeduplication": "true"
    }'

echo "Filas criadas com sucesso!"