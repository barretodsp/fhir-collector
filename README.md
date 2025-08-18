# fhir-collector


# Registro de Decisão de Arquitetura: Serviço Collector

**Status:** Proposto  
**Data:** 18/08/2025  
**Autor:** Patrick Barreto


**Versão:** 1.0

---

## Contexto

O serviço foi desenvolvido para coletar dados médicos no padrão FHIR do servidor hapi.fhir.org, que apresenta diversas limitações técnicas.

 Limitações observadas na API:
- Timeouts em consultas que envolvem múltiplos filtros
- Dados incompletos ou com estrutura inconsistente
- Respostas lentas durante períodos de alta demanda
- Ausência de garantias de disponibilidade (SLA)
- Timeout de 20s.



## Decisão

### Componentes Principais da Arquitetura

1. **Processamento Baseado em Datas**
   - Itera dia a dia através do intervalo de datas
   - Evita consultas complexas que causam timeouts na API
   - Exemplo: `GET /Encounter?date=2025-01-01`

2. **Gerenciamento de Estado com Redis**
   - Rastreia última data processada (`last_processed_date`)
   - Armazena datas com falha após retentativas para posterior reprocessamento (`unprocessed_dates`)
   - Registra o FullURL dos Encounters inválidos para posterior reprocessamento (`invalid_encounters`)

3. **Padrões de Resiliência**
   - Em caso de interrupção, serviço retoma o processamento do ponto de interrupção (última data processada)
   - Retentativas com backoff exponencial (até 3 tentativas)
   - Timeout para requisições HTTP (20 segundos)
   - Validação de códigos de status das respostas da API

4. **Processamento Paralelo**
   - Processamento concorrente de encontros usando goroutines e WaitGroup

5. **Logs Estruturados**
   - Logs em múltiplos destinos (stdout + arquivos rotacionados)
   - Logs detalhados dos passos de processamento e erros
   - Rotacionamento a cada 24hs e persistência dos últimos 3 arquivos de logs

6. **Uso de Fila FIFO**
   - Desacoplar o Processo de Coleta e Validação (collector) do Processo de Ingestão de dados (worker).

## Consequências

### Vantagens

- **Resiliência:** Mecanismos de retentativa e rastreamento de estado tornam o serviço robusto contra falhas.

- **Escalabilidade:** Processamento paralelo e uso de Fila permite lidar com grandes volumes de encontros.

- **Manutenibilidade:** Separação clara de responsabilidades e logs estruturados facilitam depuração.

- **Recuperabilidade:**: Rastreamento de estado no Redis permite retomar do último ponto bem-sucedido.

- **Auditoria:** Histórico de processamento no Redis e Logs de todas as operações.


## Trade-offs


 - Dependência do Redis torna-se um componente crítico.
 - Processamento dia a dia pode ser mais lento que consultas agregadas (mitigado pelo processamento paralelo).


## Nota Final
A estratégia adotada prioriza resiliência e confiabilidade, garantindo que os dados sejam coletados e processados mesmo em condições adversas. A utilização de cache e retentativas transforma limitações da API em problemas gerenciáveis, sem comprometer a integridade do pipeline.
