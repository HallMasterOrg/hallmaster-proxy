import process from 'node:process';
import { ShardingManager } from 'discord.js';

const DISCORD_BOT_TOKEN = process.env.DISCORD_BOT_TOKEN;

if (undefined === DISCORD_BOT_TOKEN) {
  throw new Error(`No Discord bot token found in environment variables.`);
}

const TOTAL_SHARDS = process.env.TOTAL_SHARDS;
if (undefined === TOTAL_SHARDS) {
  throw new Error(
    `The number of total shard is not found in environment variables.`,
  );
}

const SHARD_ID_LIST = process.env.SHARD_ID_LIST;
if (undefined === SHARD_ID_LIST) {
  throw new Error(
    `The comma-separated shard ID list is not found in environmnet variables.`,
  );
}

const manager = new ShardingManager('./src/index.ts', {
  token: DISCORD_BOT_TOKEN,
  totalShards: +TOTAL_SHARDS,
  shardList: SHARD_ID_LIST.split(',').map((shard) => +shard.trim()),
  shardArgs: [DISCORD_BOT_TOKEN],
});

manager.on('shardCreate', (shard) => {
  shard.on('ready', () => {
    console.log(`[SHARD MANAGER]: Shard ${shard.id} is ready !`);
  });

  shard.on('death', () => {
    console.log(`[SHARD MANAGER]: Shard ${shard.id} is dead !`);
  });

  shard.on('error', (err) => {
    console.log(
      `[SHARD MANAGER]: An error occured on Shard ${shard.id} ! ${err}`,
    );
  });
});

function shutdown() {
  console.log('\nShutting down the Discord bot shards...');
  manager.shards.forEach((shard) => {
    shard.kill();
  });
  process.exit(0);
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);

manager.spawn().catch(console.error);
