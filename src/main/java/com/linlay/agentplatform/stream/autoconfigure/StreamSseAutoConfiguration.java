package com.linlay.agentplatform.stream.autoconfigure;

import com.linlay.agentplatform.stream.service.StreamEventAssembler;
import com.linlay.agentplatform.stream.service.StreamSseStreamer;
import com.linlay.agentplatform.stream.service.SseFlushWriter;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.http.codec.ServerSentEvent;
import reactor.core.publisher.Flux;

@AutoConfiguration
@ConditionalOnClass({Flux.class, ServerSentEvent.class, ObjectMapper.class})
@EnableConfigurationProperties(StreamSseProperties.class)
public class StreamSseAutoConfiguration {

    @Bean
    @ConditionalOnMissingBean
    public ObjectMapper streamObjectMapper() {
        return new ObjectMapper();
    }

    @Bean
    @ConditionalOnMissingBean
    public StreamEventAssembler streamEventAssembler() {
        return new StreamEventAssembler();
    }

    @Bean
    @ConditionalOnMissingBean
    public StreamSseStreamer streamSseStreamer(StreamEventAssembler eventAssembler, ObjectMapper objectMapper, StreamSseProperties properties) {
        return new StreamSseStreamer(eventAssembler, objectMapper, properties.streamTimeout(), properties.heartbeatInterval());
    }

    @Bean
    @ConditionalOnMissingBean
    public SseFlushWriter sseFlushWriter() {
        return new SseFlushWriter();
    }
}
