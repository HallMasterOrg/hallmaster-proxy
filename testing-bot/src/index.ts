import process from 'node:process';
import {
  Client,
  Events,
  GatewayIntentBits,
  REST,
  Routes,
  SlashCommandBuilder,
} from 'discord.js';

const DISCORD_BOT_TOKEN = process.env['DISCORD_BOT_TOKEN'];

if (undefined === DISCORD_BOT_TOKEN) {
  throw new Error(`No Discord bot token found in environment variables.`);
}

const COMMANDS = [
  new SlashCommandBuilder()
    .setName('echo')
    .setDescription('Repeats whatever you say')
    .addStringOption((option) =>
      option
        .setName('text')
        .setDescription('The text to echo back')
        .setRequired(true),
    )
    .toJSON(),
  new SlashCommandBuilder()
    .setName('log')
    .setDescription('Logs a message to the console, with STDERR or STDOUT')
    .addStringOption((option) =>
      option
        .setName('message')
        .setDescription('The message to log')
        .setRequired(true),
    )
    .addBooleanOption((option) =>
      option
        .setName('stderr')
        .setDescription('Log to STDERR instead of STDOUT')
        .setRequired(false),
    )
    .toJSON(),
];

async function registerCommands(clientId: string) {
  const rest = new REST({ version: '10' }).setToken(DISCORD_BOT_TOKEN!);

  try {
    console.log('Started refreshing application (/) commands...');

    await rest.put(Routes.applicationCommands(clientId), { body: COMMANDS });

    console.log('Successfully registered slash commands!');
  } catch (error) {
    console.error(error);
  }
}

const client = new Client({
  intents: [
    GatewayIntentBits.Guilds,
    GatewayIntentBits.GuildMessages,
    GatewayIntentBits.GuildMessageReactions,
    GatewayIntentBits.MessageContent,
  ],
});

client.on('raw', console.log);

client.on('api', console.log);

client.on('error', console.error);

client.on('warn', console.warn);

client.on('debug', console.debug);

client.once('clientReady', () => {
  console.log(`Logged in as ${client.user!.tag}`);
  registerCommands(client.user!.id).catch(console.error);
});

// eslint-disable-next-line @typescript-eslint/no-misused-promises
client.on(Events.InteractionCreate, async (interaction) => {
  if (!interaction.isChatInputCommand()) return;

  if (interaction.commandName === 'echo') {
    const text = interaction.options.getString('text', true);

    await interaction.reply({
      content: text,
      // ephemeral: true,     // uncomment if you want only the user to see it
      // allowedMentions: { parse: [] } // prevents @everyone etc.
    });
  } else if (interaction.commandName === 'log') {
    const text = interaction.options.getString('text', true);
    const stderr = interaction.options.getBoolean('stderr') ?? false;

    if (stderr) {
      console.error(text);
    } else {
      console.log(text);
    }

    await interaction.reply({
      content: `Logged "${text}" to ${stderr ? 'STDERR' : 'STDOUT'}`,
    });
  }
});

client.on('applicationCommandPermissionsUpdate', console.log);
client.on('autoModerationActionExecution', console.log);
client.on('autoModerationRuleCreate', console.log);
client.on('autoModerationRuleDelete', console.log);
client.on('autoModerationRuleUpdate', console.log);
client.on('cacheSweep', console.log);
client.on('channelCreate', console.log);
client.on('channelDelete', console.log);
client.on('channelPinsUpdate', console.log);
client.on('channelUpdate', console.log);
client.on('emojiCreate', console.log);
client.on('emojiDelete', console.log);
client.on('emojiUpdate', console.log);
client.on('entitlementCreate', console.log);
client.on('entitlementDelete', console.log);
client.on('entitlementUpdate', console.log);
client.on('guildAuditLogEntryCreate', console.log);
client.on('guildAvailable', console.log);
client.on('guildBanAdd', console.log);
client.on('guildBanRemove', console.log);
client.on('guildCreate', console.log);
client.on('guildDelete', console.log);
client.on('guildUnavailable', console.log);
client.on('guildIntegrationsUpdate', console.log);
client.on('guildMemberAdd', console.log);
client.on('guildMemberAvailable', console.log);
client.on('guildMemberRemove', console.log);
client.on('guildMembersChunk', console.log);
client.on('guildMemberUpdate', console.log);
client.on('guildUpdate', console.log);
client.on('guildSoundboardSoundCreate', console.log);
client.on('guildSoundboardSoundDelete', console.log);
client.on('guildSoundboardSoundUpdate', console.log);
client.on('guildSoundboardSoundsUpdate', console.log);
client.on('inviteCreate', console.log);
client.on('inviteDelete', console.log);
client.on('messageCreate', console.log);
client.on('messageDelete', console.log);
client.on('messagePollVoteAdd', console.log);
client.on('messagePollVoteRemove', console.log);
client.on('messageReactionRemoveAll', console.log);
client.on('messageReactionRemoveEmoji', console.log);
client.on('messageDeleteBulk', console.log);
client.on('messageReactionAdd', console.log);
client.on('messageReactionRemove', console.log);
client.on('messageUpdate', console.log);
client.on('presenceUpdate', console.log);
client.on('invalidated', console.log);
client.on('roleCreate', console.log);
client.on('roleDelete', console.log);
client.on('roleUpdate', console.log);
client.on('threadCreate', console.log);
client.on('threadDelete', console.log);
client.on('threadListSync', console.log);
client.on('threadMemberUpdate', console.log);
client.on('threadMembersUpdate', console.log);
client.on('threadUpdate', console.log);
client.on('typingStart', console.log);
client.on('userUpdate', console.log);
client.on('voiceChannelEffectSend', console.log);
client.on('voiceStateUpdate', console.log);
client.on('webhookUpdate', console.log);
client.on('webhooksUpdate', console.log);
client.on('interactionCreate', console.log);
client.on('shardDisconnect', console.log);
client.on('shardError', console.log);
client.on('shardReady', console.log);
client.on('shardReconnecting', console.log);
client.on('shardResume', console.log);
client.on('stageInstanceCreate', console.log);
client.on('stageInstanceUpdate', console.log);
client.on('stageInstanceDelete', console.log);
client.on('stickerCreate', console.log);
client.on('stickerDelete', console.log);
client.on('stickerUpdate', console.log);
client.on('subscriptionCreate', console.log);
client.on('subscriptionDelete', console.log);
client.on('subscriptionUpdate', console.log);
client.on('guildScheduledEventCreate', console.log);
client.on('guildScheduledEventUpdate', console.log);
client.on('guildScheduledEventDelete', console.log);
client.on('guildScheduledEventUserAdd', console.log);
client.on('guildScheduledEventUserRemove', console.log);
client.on('soundboardSounds', console.log);

client.login(DISCORD_BOT_TOKEN).catch(console.error);

process.on('SIGINT', () => {
  console.log('Shutting down...');
  client.destroy().catch(console.error);
  process.exit(0);
});
