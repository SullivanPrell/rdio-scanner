/*
 * *****************************************************************************
 * Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>
 * ****************************************************************************
 */

import { Component, EventEmitter, Output } from '@angular/core';
import { MatSnackBar } from '@angular/material/snack-bar';
import {
    BridgeChannelConfig,
    Config,
    Group,
    ImportResult,
    RdioScannerAdminService,
    System,
    Tag,
    Talkgroup,
} from '../../admin.service';

@Component({
    selector: 'rdio-scanner-admin-quick-add',
    templateUrl: './quick-add.component.html',
    standalone: false,
})
export class RdioScannerAdminQuickAddComponent {
    @Output() config = new EventEmitter<Config>();

    // Shared options for all import types
    portBase = 9000;

    // CHIRP-specific
    chirpFile: File | null = null;
    chirpSystemLabel = 'Imported';
    chirpLoading = false;
    chirpPreview: { talkgroupRef: number; label: string; name: string; freqHz: number }[] = [];

    loading = false;

    constructor(
        private adminService: RdioScannerAdminService,
        private matSnackBar: MatSnackBar,
    ) {}

    async addFRS(): Promise<void> {
        this.loading = true;
        const base = await this.adminService.getConfig();
        const sysRef = this.nextSystemRef(base.systems ?? []);
        const result = await this.adminService.importFRS(sysRef, this.portBase);
        this.loading = false;
        this.mergeAndEmit(base, result, 'FRS/GMRS channels added.');
    }

    async addGMRS(): Promise<void> {
        this.loading = true;
        const base = await this.adminService.getConfig();
        const sysRef = this.nextSystemRef(base.systems ?? []);
        const result = await this.adminService.importGMRS(sysRef, this.portBase);
        this.loading = false;
        this.mergeAndEmit(base, result, 'GMRS channels added (includes repeater inputs).');
    }

    onChirpFileChange(event: Event): void {
        const input = event.target as HTMLInputElement;
        const file = input.files?.item(0);
        if (!file) return;
        this.chirpFile = file;
        this.chirpSystemLabel = file.name.replace(/\.[^.]+$/, '');
        this.previewChirp(file);
    }

    private previewChirp(file: File): void {
        const reader = new FileReader();
        reader.onloadend = () => {
            if (typeof reader.result !== 'string') return;
            const lines = reader.result.split(/\r?\n/);
            this.chirpPreview = [];
            for (const line of lines) {
                if (!line || line.startsWith('#') || line.startsWith('Location')) continue;
                const cols = line.split(',').map(c => c.replace(/^"|"$/g, '').trim());
                if (cols.length < 14) continue;
                const loc = parseInt(cols[0]);
                if (isNaN(loc)) continue;
                const freqMHz = parseFloat(cols[2]);
                if (isNaN(freqMHz)) continue;
                const comment = cols.length > 16 ? cols[16] : '';
                this.chirpPreview.push({
                    talkgroupRef: loc + 1,
                    label: cols[1],
                    name: comment || cols[1],
                    freqHz: Math.round(freqMHz * 1e6),
                });
            }
        };
        reader.readAsText(file);
    }

    async importChirp(): Promise<void> {
        if (!this.chirpFile) return;
        this.chirpLoading = true;
        const base = await this.adminService.getConfig();
        const sysRef = this.nextSystemRef(base.systems ?? []);
        const result = await this.adminService.importChirp(this.chirpFile, this.chirpSystemLabel, sysRef, this.portBase);
        this.chirpLoading = false;
        if (!result?.systems?.length) {
            this.matSnackBar.open('No channels found in CSV.', '', { duration: 3000 });
            return;
        }
        this.chirpFile = null;
        this.chirpPreview = [];
        this.mergeAndEmit(base, result, `${result.systems[0]?.talkgroups?.length ?? 0} channels imported from CHIRP CSV.`);
    }

    private mergeAndEmit(base: Config, result: ImportResult, message: string): void {
        if (!result?.systems?.length) {
            this.matSnackBar.open('No data to import.', '', { duration: 3000 });
            return;
        }

        const mergedGroups = [...(base.groups ?? [])];
        const mergedTags = [...(base.tags ?? [])];

        const ensureGroup = (label: string): number => {
            let g = mergedGroups.find(g => g.label === label);
            if (!g) {
                const id = Math.max(0, ...mergedGroups.map(g => g.id ?? 0)) + 1;
                g = { id, label } as Group;
                mergedGroups.push(g);
            }
            return g.id!;
        };

        const ensureTag = (label: string): number => {
            let t = mergedTags.find(t => t.label === label);
            if (!t) {
                const id = Math.max(0, ...mergedTags.map(t => t.id ?? 0)) + 1;
                t = { id, label } as Tag;
                mergedTags.push(t);
            }
            return t.id!;
        };

        result.groups?.forEach(g => g.label && ensureGroup(g.label));
        result.tags?.forEach(t => t.label && ensureTag(t.label));

        const generalGroupId = ensureGroup('General');
        const analogTagId = ensureTag('Analog');

        const mergedSystems = [...(base.systems ?? [])];
        result.systems?.forEach(sys => {
            const talkgroups: Talkgroup[] = (sys.talkgroups ?? []).map(tg => ({
                ...tg,
                groupIds: tg.groupIds?.length ? tg.groupIds : [generalGroupId],
                tagId: tg.tagId ?? analogTagId,
            }));
            mergedSystems.push({ ...sys, talkgroups } as System);
        });

        const mergedChannels = [...(base.options?.bridgeChannels ?? [])];
        const usedPorts = new Set(mergedChannels.map(c => c.udpPort));
        result.channels?.forEach(ch => {
            if (ch.udpPort !== undefined && !usedPorts.has(ch.udpPort)) {
                mergedChannels.push(ch as BridgeChannelConfig);
                usedPorts.add(ch.udpPort);
            }
        });

        const merged: Config = {
            ...base,
            groups: mergedGroups,
            tags: mergedTags,
            systems: mergedSystems,
            options: { ...base.options, bridgeChannels: mergedChannels },
        };

        this.config.emit(merged);
        this.matSnackBar.open(message + ' Review in Config panel and Save.', '', { duration: 5000 });
    }

    private nextSystemRef(systems: System[]): number {
        return Math.max(0, ...systems.map(s => s.systemRef ?? 0)) + 1;
    }
}
