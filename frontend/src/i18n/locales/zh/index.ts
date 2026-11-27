import adminAccessKeys from './adminAccessKeys'
import mcp from './mcp'
import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import modelPlazaBoard from './modelPlazaBoard'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'
import team from './team'

export default {
  adminAccessKeys,
  mcp,
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...modelPlazaBoard,
  ...batchImage,
  admin,
  ...team,
  ...misc,
}
