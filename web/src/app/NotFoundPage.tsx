import { Button, Result } from 'antd';
import { Link } from 'react-router-dom';
import { routes } from '../shared/lib/routes';

export default function NotFoundPage() {
  return (
    <Result
      status="404"
      title="页面不存在"
      subTitle="该前端路由未定义。接口资源不存在时请回到对应列表重新查找。"
      extra={
        <Link to={routes.overview}>
          <Button type="primary">返回概览</Button>
        </Link>
      }
    />
  );
}
