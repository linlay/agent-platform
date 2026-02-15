package com.linlay.springaiagw.service;

import com.linlay.springaiagw.agent.AgentCatalogProperties;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.config.CapabilityCatalogProperties;
import com.linlay.springaiagw.config.ViewportCatalogProperties;
import com.linlay.springaiagw.skill.SkillCatalogProperties;
import com.linlay.springaiagw.skill.SkillRegistryService;
import com.linlay.springaiagw.tool.CapabilityRegistryService;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.DisposableBean;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

import java.io.IOException;
import java.nio.file.ClosedWatchServiceException;
import java.nio.file.FileSystems;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardWatchEventKinds;
import java.nio.file.WatchEvent;
import java.nio.file.WatchKey;
import java.nio.file.WatchService;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.TimeUnit;

@Service
public class DirectoryWatchService implements DisposableBean {

    private static final Logger log = LoggerFactory.getLogger(DirectoryWatchService.class);
    private static final long DEBOUNCE_MS = 500;

    private final AgentRegistry agentRegistry;
    private final ViewportRegistryService viewportRegistryService;
    private final CapabilityRegistryService capabilityRegistryService;
    private final SkillRegistryService skillRegistryService;
    private final Map<Path, Runnable> watchedDirs;

    private volatile WatchService watchService;
    private volatile Thread watchThread;

    @Autowired
    public DirectoryWatchService(
            AgentRegistry agentRegistry,
            ViewportRegistryService viewportRegistryService,
            CapabilityRegistryService capabilityRegistryService,
            SkillRegistryService skillRegistryService,
            AgentCatalogProperties agentCatalogProperties,
            ViewportCatalogProperties viewportCatalogProperties,
            CapabilityCatalogProperties capabilityCatalogProperties,
            SkillCatalogProperties skillCatalogProperties
    ) {
        this.agentRegistry = agentRegistry;
        this.viewportRegistryService = viewportRegistryService;
        this.capabilityRegistryService = capabilityRegistryService;
        this.skillRegistryService = skillRegistryService;

        this.watchedDirs = new LinkedHashMap<>();
        watchedDirs.put(
                Path.of(agentCatalogProperties.getExternalDir()).toAbsolutePath().normalize(),
                agentRegistry::refreshAgents
        );
        watchedDirs.put(
                Path.of(viewportCatalogProperties.getExternalDir()).toAbsolutePath().normalize(),
                viewportRegistryService::refreshViewports
        );
        watchedDirs.put(
                Path.of(capabilityCatalogProperties.getToolsExternalDir()).toAbsolutePath().normalize(),
                capabilityRegistryService::refreshCapabilities
        );
        watchedDirs.put(
                Path.of(skillCatalogProperties.getExternalDir()).toAbsolutePath().normalize(),
                skillRegistryService::refreshSkills
        );

        start();
    }

    // visible for testing
    DirectoryWatchService(
            AgentRegistry agentRegistry,
            ViewportRegistryService viewportRegistryService,
            CapabilityRegistryService capabilityRegistryService,
            SkillRegistryService skillRegistryService,
            Map<Path, Runnable> watchedDirs
    ) {
        this.agentRegistry = agentRegistry;
        this.viewportRegistryService = viewportRegistryService;
        this.capabilityRegistryService = capabilityRegistryService;
        this.skillRegistryService = skillRegistryService;
        this.watchedDirs = watchedDirs;
        start();
    }

    private void start() {
        try {
            watchService = FileSystems.getDefault().newWatchService();
        } catch (IOException ex) {
            log.warn("Cannot create WatchService, directory watching disabled", ex);
            return;
        }

        Map<WatchKey, Runnable> keyActions = new LinkedHashMap<>();
        for (Map.Entry<Path, Runnable> entry : watchedDirs.entrySet()) {
            Path dir = entry.getKey();
            if (!Files.isDirectory(dir)) {
                log.debug("Directory does not exist, skip watching: {}", dir);
                continue;
            }
            try {
                WatchKey key = dir.register(watchService,
                        StandardWatchEventKinds.ENTRY_CREATE,
                        StandardWatchEventKinds.ENTRY_MODIFY,
                        StandardWatchEventKinds.ENTRY_DELETE);
                keyActions.put(key, entry.getValue());
                log.info("Watching directory: {}", dir);
            } catch (IOException ex) {
                log.warn("Cannot register watch on {}", dir, ex);
            }
        }

        if (keyActions.isEmpty()) {
            closeWatchService();
            return;
        }

        watchThread = new Thread(() -> pollLoop(keyActions), "dir-watch");
        watchThread.setDaemon(true);
        watchThread.start();
    }

    private void pollLoop(Map<WatchKey, Runnable> keyActions) {
        Map<Runnable, Long> lastTrigger = new LinkedHashMap<>();

        while (!Thread.currentThread().isInterrupted()) {
            WatchKey key;
            try {
                key = watchService.poll(2, TimeUnit.SECONDS);
            } catch (ClosedWatchServiceException | InterruptedException ex) {
                break;
            }
            if (key == null) {
                continue;
            }

            for (WatchEvent<?> event : key.pollEvents()) {
                // drain events
            }

            boolean valid = key.reset();
            Runnable action = keyActions.get(key);

            if (action != null) {
                long now = System.currentTimeMillis();
                Long last = lastTrigger.get(action);
                if (last == null || now - last >= DEBOUNCE_MS) {
                    lastTrigger.put(action, now);
                    try {
                        action.run();
                    } catch (Exception ex) {
                        log.warn("Error executing refresh callback", ex);
                    }
                }
            }

            if (!valid) {
                keyActions.remove(key);
                if (keyActions.isEmpty()) {
                    break;
                }
            }
        }
    }

    private void closeWatchService() {
        WatchService ws = watchService;
        if (ws != null) {
            try {
                ws.close();
            } catch (IOException ex) {
                log.debug("Error closing WatchService", ex);
            }
        }
    }

    @Override
    public void destroy() {
        closeWatchService();
        Thread t = watchThread;
        if (t != null) {
            t.interrupt();
        }
    }
}
